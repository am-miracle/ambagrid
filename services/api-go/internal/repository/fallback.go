// Package repository defines storage contracts used by the application layer.
package repository

import (
	"context"
	"time"

	"api-go/internal/domain"
)

type FallbackRepository interface {
	RunFallbackTx(ctx context.Context, eventKey domain.EventKey, fn func(FallbackTx) (bool, error)) (bool, error)
}

type FallbackTx interface {
	FindFallbackEvent(ctx context.Context, eventKey domain.EventKey) (*domain.StoredFallbackEvent, error)
	LookupAssetType(ctx context.Context, assetID, siteID string) (string, error)
	FindAlertBySourceEvent(ctx context.Context, sourceEventID domain.EventKey) (string, error)
	FindOpenAlert(ctx context.Context, siteID, assetID, kind string) (string, error)
	CreateAlert(ctx context.Context, cmd domain.CreateAlertCommand) (string, error)
	PublishOutboxEvent(ctx context.Context, topic, eventType, aggregateType, aggregateID string, payload []byte) error
	InsertFallbackEvent(ctx context.Context, cmd domain.InsertFallbackEventCommand) error
	InsertReceipt(ctx context.Context, eventKey domain.EventKey, receipt domain.SMSReceipt, pricing domain.SMSPricing) (bool, error)
	FindReceiptEventKey(ctx context.Context, providerMessageID string) (domain.EventKey, error)
	GetMonthlySMSUsage(ctx context.Context, at time.Time, rentalMinor int64) (int64, error)
}
