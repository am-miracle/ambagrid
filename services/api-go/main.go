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
	"api-go/internal/domain"
	"api-go/internal/repository/postgres"
	"api-go/internal/services"
	"api-go/internal/webhook"
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

	store := postgres.NewStoreWithOutboxTopics(pool, cfg.DBQueryTimeout, postgres.OutboxTopics{
		AlertResolved:         cfg.AlertResolvedTopic,
		PaymentConfirmed:      cfg.PaymentConfirmedTopic,
		CreditIssued:          cfg.CreditIssuedTopic,
		MeterCommandRequested: cfg.MeterCommandRequestedTopic,
	}).WithMeterCommandTTL(cfg.MeterCommandTTL)
	limits := services.PageLimits{
		DefaultSize: cfg.DefaultPageSize,
		MaxSize:     cfg.MaxPageSize,
	}
	// unscoped alert history uses a smaller fixed page size.
	globalHistoryLimits := services.PageLimits{
		DefaultSize: cfg.GlobalHistoryPageSize,
		MaxSize:     cfg.GlobalHistoryPageSize,
	}

	webhooks := buildWebhookRoutes(cfg, logger)
	var fallbackService controller.FallbackService
	var heartbeatService controller.HeartbeatService
	var smsWebhook controller.SMSWebhookProvider
	if cfg.SMSEnabled {
		senders := make(map[string]webhook.SMSSender, len(cfg.SMSSenders))
		for number, identity := range cfg.SMSSenders {
			senders[number] = webhook.SMSSender{SiteID: identity.SiteID, GatewayID: identity.GatewayID}
		}
		fallbackStore := postgres.NewFallbackStore(pool)
		smsFallback := services.NewFallbackService(fallbackStore, cfg.AlertOpenedTopic, domain.SMSPricing{
			OutboundCostMinor: cfg.SMSOutboundCostMinor, InboundCostMinor: cfg.SMSInboundCostMinor,
			NumberRentalMinor: cfg.SMSNumberRentalMinor, MonthlyBudgetMinor: cfg.SMSMonthlyBudgetMinor,
			Currency: cfg.SMSCurrency,
		})
		fallbackService = smsFallback
		heartbeatService = smsFallback.Heartbeats(postgres.NewHeartbeatStore(pool))
		smsWebhook, err = webhook.NewSMS(cfg.SMSWebhookSecret, senders)
		if err != nil {
			logger.Error("configure SMS webhook", "error", err)
			os.Exit(1)
		}
		logger.Info("SMS fallback webhook enabled", "path", "/v1/sms/inbound")
	}

	var edgeCommands controller.EdgeCommandService
	if len(cfg.EdgeAPIKeys) > 0 {
		edgeCommands = services.NewEdgeCommandService(postgres.NewEdgeCommandStore(pool, cfg.DBQueryTimeout), services.OutcomeTopics{
			Acknowledged: cfg.CommandAcknowledgedTopic, Failed: cfg.CommandFailedTopic,
		})
		logger.Info("edge command delivery enabled", "path", "/v1/edge/commands", "sites", len(cfg.EdgeAPIKeys))
	}

	api := controller.API{
		Assets: services.NewAssetService(store, limits),
		Alerts: services.NewAlertService(store, limits, globalHistoryLimits),
		Sites: services.NewSiteService(store, limits, services.SiteHealthThresholds{
			OfflineAfter: cfg.SiteOfflineAfter, EventStaleAfter: cfg.SiteEventStaleAfter,
		}),
		Payments:       services.NewPaymentService(store),
		Health:         services.NewHealthService(store),
		Webhooks:       webhooks,
		Logger:         logger,
		RequestTimeout: cfg.RequestTimeout,
		AllowedOrigins: cfg.CORSAllowedOrigins,
		DevMode:        cfg.DevMode,
		Fallback:       fallbackService,
		Heartbeats:     heartbeatService,
		SMSWebhook:     smsWebhook,
		EdgeCommands:   edgeCommands,
		EdgeAuth:       controller.NewSiteKeys(cfg.EdgeAPIKeys),
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

func buildWebhookRoutes(cfg config.Config, logger *slog.Logger) []controller.WebhookRoute {
	var routes []controller.WebhookRoute

	if cfg.PaystackSecretKey != "" {
		paystack, err := webhook.NewPaystack(cfg.PaystackSecretKey)
		if err != nil {
			logger.Error("configure paystack webhook", "error", err)
			os.Exit(1)
		}
		routes = append(routes, controller.WebhookRoute{
			Pattern:  "/v1/webhooks/paystack",
			Provider: paystack,
		})
		logger.Info("paystack webhook enabled", "path", "/v1/webhooks/paystack")
	}

	return routes
}

// newLogger uses JSON unless text output is requested.
func newLogger() *slog.Logger {
	if os.Getenv("LOG_FORMAT") == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, nil))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}
