package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"ingestion-go/internal/bridge"
	"ingestion-go/internal/config"
	"ingestion-go/internal/ingest"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	errs := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := bridge.Run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
		}
	}()

	if cfg.HTTP.Enabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runHTTPIngest(ctx, cfg); err != nil {
				errs <- err
			}
		}()
	}

	go func() {
		wg.Wait()
		close(errs)
	}()

	for err := range errs {
		log.Fatalf("fatal: %v", err)
	}
}

func runHTTPIngest(ctx context.Context, cfg config.Config) error {
	logger := slog.Default()

	kafkaClient, err := bridge.NewKafkaClient(cfg)
	if err != nil {
		return err
	}
	defer kafkaClient.Close()

	pool, err := pgxpool.New(ctx, cfg.HTTP.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}

	authEntries := make([]ingest.APIKeyEntry, len(cfg.HTTP.APIKeys))
	for i, e := range cfg.HTTP.APIKeys {
		authEntries[i] = ingest.APIKeyEntry{Key: e.Key, SiteID: e.SiteID}
	}

	handler := ingest.NewHandler(ingest.HandlerConfig{
		Auth:     ingest.NewAPIKeyAuth(authEntries),
		Store:    ingest.NewMemoryDedupStore(),
		Producer: ingest.NewKafkaProducer(kafkaClient, cfg.KafkaTopic, cfg.ProduceTimeout),
		Health:   ingest.NewPostgresSiteHealthStore(pool),
		Logger:   logger,
		MaxBody:  cfg.HTTP.MaxBodyBytes,
	})

	return ingest.Serve(ctx, ingest.ServerConfig{
		Addr:              cfg.HTTP.Addr,
		ReadHeaderTimeout: cfg.ProduceTimeout,
		WriteTimeout:      cfg.ProduceTimeout * 2,
		IdleTimeout:       cfg.ProduceTimeout * 6,
		ShutdownTimeout:   cfg.HTTP.ShutdownTimeout,
		Logger:            logger,
	}, handler)
}
