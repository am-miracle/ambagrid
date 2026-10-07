// edge-agent-go runs on-site near physical hardware. It collects readings from
// meters, batteries, and inverters, persists them in a SQLite durable queue,
// and uploads batches to the ingestion service over HTTP. The queue survives
// power loss and connectivity gaps — records are only removed after the server
// acknowledges them. All configuration is environment-based so the same binary
// runs across sites with different identities and endpoints.
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
	"edge-agent-go/internal/uploader"
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

	var workerWG sync.WaitGroup
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer func() {
		cancelWorkers()
		workerWG.Wait()
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
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			telemetryCollector.Run(workerCtx)
		}()
	}

	if cfg.Uploader.Enabled {
		client := uploader.NewIngestClient(
			cfg.Uploader.Endpoint,
			cfg.Uploader.APIKey,
			cfg.Uploader.Timeout,
		)
		ul := uploader.New(uploader.Config{
			GatewayID:     cfg.Queue.GatewayID,
			BatchSize:     cfg.Uploader.BatchSize,
			BatchMaxBytes: cfg.Uploader.BatchMaxBytes,
			PollInterval:  cfg.Uploader.PollInterval,
			BaseDelay:     cfg.Uploader.BaseDelay,
			MaxDelay:      cfg.Uploader.MaxDelay,
			MaxRetries:    cfg.Uploader.MaxRetries,
		}, durableQueue, client)
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			slog.Info("uploader started", "endpoint", cfg.Uploader.Endpoint)
			ul.Run(workerCtx)
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
