// starts the operator API and wires its dependencies.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"api-go/internal/config"
	"api-go/internal/controller"
	"api-go/internal/repository/postgres"
	"api-go/internal/services"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	logger := newLogger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		logger.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	store := postgres.NewStore(pool, cfg.DBQueryTimeout)
	limits := services.PageLimits{
		DefaultSize: cfg.DefaultPageSize,
		MaxSize:     cfg.MaxPageSize,
	}
	// unscoped alert history uses a smaller fixed page size.
	globalHistoryLimits := services.PageLimits{
		DefaultSize: cfg.GlobalHistoryPageSize,
		MaxSize:     cfg.GlobalHistoryPageSize,
	}

	api := controller.API{
		Assets:         services.NewAssetService(store, limits),
		Alerts:         services.NewAlertService(store, limits, globalHistoryLimits),
		Sites:          services.NewSiteService(store, limits),
		Health:         services.NewHealthService(store),
		Logger:         logger,
		RequestTimeout: cfg.RequestTimeout,
		AllowedOrigins: cfg.CORSAllowedOrigins,
	}

	err = controller.Serve(ctx, controller.ServerOptions{
		Addr:              cfg.HTTPAddr,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		// leave enough time to return the API's timeout response.
		WriteTimeout:    cfg.RequestTimeout + cfg.ReadHeaderTimeout,
		IdleTimeout:     cfg.IdleTimeout,
		ShutdownTimeout: cfg.ShutdownTimeout,
		Logger:          logger,
	}, api.Handler())
	if err != nil {
		logger.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

// newLogger uses JSON unless text output is requested.
func newLogger() *slog.Logger {
	if os.Getenv("LOG_FORMAT") == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, nil))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}
