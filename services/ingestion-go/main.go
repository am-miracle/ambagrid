package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"ingestion-go/internal/bridge"
	"ingestion-go/internal/config"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := bridge.Run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("ingestion bridge stopped: %v", err)
	}
}
