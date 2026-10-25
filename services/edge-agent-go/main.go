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

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/mqtt"
	"edge-agent-go/internal/adapter/simulator"
	"edge-agent-go/internal/adapter/victron"
	"edge-agent-go/internal/collector"
	"edge-agent-go/internal/commands"
	"edge-agent-go/internal/config"
	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/fallback"
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

	var meter *simulator.Adapter
	var sources []collector.Source
	if cfg.Collector.Enabled {
		simulatorOptions := []simulator.Option{}
		if cfg.Collector.Faults {
			simulatorOptions = append(simulatorOptions, simulator.WithCriticalFaults())
		}
		// One instance serves telemetry and commands, so relay changes show up in readings.
		meter = simulator.New(cfg.Queue.SiteID, simulatorOptions...)
		sources = append(sources, meter)
	}
	if cfg.Victron.Address != "" {
		battery, err := victron.New(victron.Config{SiteID: cfg.Queue.SiteID, DeviceID: cfg.Victron.DeviceID, UnitID: cfg.Victron.UnitID}, &victron.TCP{Address: cfg.Victron.Address})
		if err != nil {
			return err
		}
		sources = append(sources, battery)
	}
	if len(sources) > 0 {
		telemetryCollector, err := collector.New(collector.Config{
			SiteID:           cfg.Queue.SiteID,
			Region:           cfg.Collector.Region,
			Interval:         cfg.Collector.Interval,
			BatteryOverheatC: cfg.SMS.BatteryOverheatC,
			FallbackDebounce: cfg.SMS.Debounce,
		}, durableQueue, sources...)
		if err != nil {
			return fmt.Errorf("configure telemetry collector: %w", err)
		}
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			telemetryCollector.Run(workerCtx)
		}()

	}

	if cfg.Commands.Enabled {
		commandAdapter, closeAdapter, err := newCommandAdapter(ctx, cfg, meter)
		if err != nil {
			return fmt.Errorf("configure meter command adapter: %w", err)
		}
		defer closeAdapter()
		runner := commands.New(
			commands.Config{PollInterval: cfg.Commands.PollInterval, CommandTimeout: cfg.Commands.Timeout},
			commands.NewHTTPClient(cfg.Commands.Endpoint, cfg.Commands.APIKey, cfg.Commands.Timeout),
			adapter.NewLedger(durableQueue, commandAdapter), durableQueue,
		)
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			slog.Info("meter command delivery started", "endpoint", cfg.Commands.Endpoint, "adapter", cfg.Commands.Adapter)
			runner.Run(workerCtx)
		}()
	}

	if cfg.SMS.Enabled {
		coordinator := fallback.NewCoordinator(fallback.Config{
			Destination: cfg.SMS.Destination, PollInterval: cfg.SMS.PollInterval,
			FailureThreshold: cfg.SMS.FailureThreshold, OfflineAfter: cfg.SMS.OfflineAfter,
			MaxAttempts: cfg.SMS.MaxAttempts, HourlyLimit: cfg.SMS.HourlyLimit,
			DailyLimit: cfg.SMS.DailyLimit, RetryInterval: cfg.SMS.RetryInterval,
		}, durableQueue, fallback.NewModemManagerSender(cfg.SMS.ModemID, nil, cfg.SMS.SendTimeout))
		if cfg.SMS.HeartbeatEnabled {
			coordinator.EnableHeartbeat(fallback.HeartbeatConfig{
				SiteID: cfg.Queue.SiteID, GatewayID: cfg.Queue.GatewayID,
				Interval: cfg.SMS.HeartbeatInterval, DailyLimit: cfg.SMS.HeartbeatDailyLimit,
			}, durableQueue)
		}
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			slog.Info("SMS fallback started", "modem", cfg.SMS.ModemID)
			coordinator.Run(workerCtx)
		}()
	}

	workerWG.Add(1)
	go func() {
		defer workerWG.Done()
		runPurge(workerCtx, durableQueue, cfg.Queue.PurgeRetention, cfg.Queue.PurgeInterval)
	}()
	workerWG.Add(1)
	go func() {
		defer workerWG.Done()
		runCommandPrune(workerCtx, durableQueue, cfg.Commands.Retention, cfg.Commands.PruneInterval)
	}()

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
		}, durableQueue, client, durableQueue)
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			slog.Info("uploader started", "endpoint", cfg.Uploader.Endpoint)
			ul.Run(workerCtx)
		}()
	}

	server := &http.Server{
		Addr: cfg.HealthAddr,
		Handler: health.NewHandler(durableQueue, domain.FallbackRateLimitPolicy{
			MaxAttempts: cfg.SMS.MaxAttempts, HourlyLimit: cfg.SMS.HourlyLimit, DailyLimit: cfg.SMS.DailyLimit,
		}),
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

func runPurge(ctx context.Context, queue domain.Queue, retention, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := queue.Purge(ctx, retention)
			if err != nil {
				slog.Error("purge acknowledged records", "error", err)
			} else if deleted > 0 {
				slog.Info("purged acknowledged records", "deleted", deleted)
			}
		}
	}
}

const commandPruneBatch = 500

type commandPruner interface {
	PruneCommands(ctx context.Context, cutoff time.Time, limit int) (int64, error)
}

// runCommandPrune deletes resolved commands in batches so a large backlog never
// holds the queue's single SQLite connection for long.
func runCommandPrune(ctx context.Context, store commandPruner, retention, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-retention)
			var total int64
			for ctx.Err() == nil {
				deleted, err := store.PruneCommands(ctx, cutoff, commandPruneBatch)
				if err != nil {
					slog.Error("prune resolved commands", "error", err)
					break
				}
				total += deleted
				if deleted < commandPruneBatch {
					break
				}
			}
			if total > 0 {
				slog.Info("pruned resolved commands", "deleted", total)
			}
		}
	}
}

// newCommandAdapter returns the configured adapter and a function releasing
// its connection; config validation guarantees meter is set for the simulator.
func newCommandAdapter(ctx context.Context, cfg config.Config, meter *simulator.Adapter) (adapter.CommandAdapter, func(), error) {
	if cfg.Commands.Adapter == "simulator" {
		return meter, func() {}, nil
	}
	m := cfg.Commands.MQTT
	caps, err := adapter.LoadCapabilities(m.CapabilitiesFile)
	if err != nil {
		return nil, nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport, err := mqtt.DialPaho(dialCtx, mqtt.PahoConfig{
		Broker: m.Broker, ClientID: m.ClientID, Username: m.Username, Password: m.Password, CAFile: m.CAFile,
	})
	if err != nil {
		return nil, nil, err
	}
	commandAdapter, err := mqtt.New(dialCtx, mqtt.Config{TopicPrefix: m.TopicPrefix, Capabilities: caps}, transport)
	if err != nil {
		transport.Close()
		return nil, nil, err
	}
	return commandAdapter, transport.Close, nil
}
