package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"edge-agent-go/internal/adapter/simulator"
	"edge-agent-go/internal/collector"
	"edge-agent-go/internal/config"
	"edge-agent-go/internal/health"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

func main() {
	if err := run(); err != nil {
		slog.Error("edge agent stopped", "error", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("load edge agent configuration: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	durableQueue, err := queuesqlite.Open(ctx, queuesqlite.Config{
		Path:                   cfg.Queue.Path,
		SiteID:                 cfg.Queue.SiteID,
		GatewayID:              cfg.Queue.GatewayID,
		MaxStorageBytes:        cfg.Queue.MaxStorageBytes,
		MaxEventBytes:          cfg.Queue.MaxEventBytes,
		WarningPercent:         cfg.Queue.WarningPercent,
		CriticalReservePercent: cfg.Queue.CriticalReservePercent,
		MinFilesystemFreeBytes: cfg.Queue.MinFilesystemFreeBytes,
	})
	if err != nil {
		return fmt.Errorf("open durable queue: %w", err)
	}
	defer func() {
		if err := durableQueue.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close durable queue: %w", err))
		}
	}()

	var collectorWG sync.WaitGroup
	collectorCtx, cancelCollector := context.WithCancel(ctx)
	defer func() {
		cancelCollector()
		collectorWG.Wait()
	}()
	if cfg.Collector.Enabled {
		telemetryCollector, err := collector.New(collector.Config{
			SiteID:   cfg.Queue.SiteID,
			Region:   cfg.Collector.Region,
			Interval: cfg.Collector.Interval,
		}, durableQueue, simulator.New())
		if err != nil {
			return fmt.Errorf("configure telemetry collector: %w", err)
		}
		collectorWG.Add(1)
		go func() {
			defer collectorWG.Done()
			telemetryCollector.Run(collectorCtx)
		}()
	}

	server := &http.Server{
		Addr:              cfg.HealthAddr,
		Handler:           health.NewHandler(durableQueue),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("edge queue health server listening", "address", cfg.HealthAddr)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down health server: %w", err)
		}
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve edge queue health: %w", err)
	}
}
