package repository

import (
	"context"
	"time"

	"api-go/internal/domain"
)

type HeartbeatRepository interface {
	RunHeartbeatTx(ctx context.Context, key domain.HeartbeatKey, fn func(HeartbeatTx) (bool, error)) (bool, error)
}

type HeartbeatTx interface {
	FindHeartbeat(ctx context.Context, key domain.HeartbeatKey) (*domain.StoredHeartbeat, error)
	InsertHeartbeat(ctx context.Context, heartbeat domain.GatewayHeartbeat, contactAt time.Time) error
	InsertHeartbeatReceipt(ctx context.Context, key domain.HeartbeatKey, receipt domain.SMSReceipt, pricing domain.SMSPricing) (bool, error)
	FindHeartbeatReceiptKey(ctx context.Context, providerMessageID string) (domain.HeartbeatKey, error)
	GetMonthlySMSUsage(ctx context.Context, at time.Time, rentalMinor int64) (int64, error)
}
