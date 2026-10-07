package services

import (
	"context"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
)

type AssetRepository interface {
	ListAssets(ctx context.Context, query domain.AssetQuery) (page.Page[domain.Asset], error)
	GetAsset(ctx context.Context, assetID string) (domain.Asset, error)
	GetReadingSeries(ctx context.Context, query domain.ReadingQuery) (domain.ReadingSeries, error)
}

type AlertRepository interface {
	ListAlerts(ctx context.Context, query domain.AlertQuery) (page.Page[domain.Alert], error)
	GetAlertWithResolutions(ctx context.Context, alertID string) (domain.Alert, []domain.AlertResolution, error)
	ResolveAlert(ctx context.Context, command domain.ResolveAlertCommand) (domain.Alert, error)
}

type SiteRepository interface {
	ListSites(ctx context.Context, query domain.SiteQuery) (page.Page[domain.Site], error)
}

type PaymentRepository interface {
	RunPaymentTx(ctx context.Context, fn func(PaymentTx) (domain.ApplyPaymentResult, error)) (domain.ApplyPaymentResult, error)
	ListCustomerSummaries(ctx context.Context) ([]domain.CustomerSummary, error)
	ListMeterCommands(ctx context.Context) ([]domain.MeterCommand, error)
	ListAuditEvents(ctx context.Context, customerID string) ([]domain.AuditEvent, error)
}

type PaymentTx interface {
	FindExistingPayment(ctx context.Context, provider, externalReference string) (*domain.Payment, error)
	InsertPayment(ctx context.Context, command domain.ApplyPaymentCommand) (*domain.Payment, error)
	ReplayPriorResult(ctx context.Context, payment domain.Payment) (domain.ApplyPaymentResult, error)
	FindActiveAssignment(ctx context.Context, customerID string) (domain.MeterAssignment, error)
	FindActiveTariff(ctx context.Context, siteID string, at time.Time) (domain.Tariff, error)
	InsertEnergyCredit(ctx context.Context, siteID, assignmentID, paymentID, tariffPlanID string, kwhGranted float64, moneyMinorUnits int64) (domain.EnergyCredit, error)
	GetPriorBalance(ctx context.Context, assignmentID string) (float64, error)
	UpsertCreditBalance(ctx context.Context, assignmentID string, kwhGranted float64, moneyMinorUnits int64) (domain.CreditBalance, error)
	InsertMeterCommand(ctx context.Context, meterID string, commandType domain.MeterCommandType, requestedBy, reason string) (domain.MeterCommand, error)
	InsertPaymentConfirmedEvent(ctx context.Context, payment domain.Payment) error
	InsertCreditIssuedEvent(ctx context.Context, credit domain.EnergyCredit) error
	InsertMeterCommandEvent(ctx context.Context, cmd domain.MeterCommand, siteID, assignmentID string) error
	InsertAuditEvent(ctx context.Context, siteID, actorID, action, subjectType, subjectID string, metadata map[string]any) error
}

type HealthRepository interface {
	Ping(ctx context.Context) error
}

type FallbackRepository interface {
	ReceiveSMS(context.Context, domain.CriticalFallbackEvent, domain.SMSReceipt) (bool, error)
}
