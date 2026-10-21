package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"api-go/internal/domain"
	"api-go/internal/repository"
)

type HeartbeatStore struct {
	pool databasePool
}

func NewHeartbeatStore(pool databasePool) *HeartbeatStore {
	return &HeartbeatStore{pool: pool}
}

func (s *HeartbeatStore) RunHeartbeatTx(ctx context.Context, key domain.HeartbeatKey, fn func(repository.HeartbeatTx) (bool, error)) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin heartbeat tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return false, fmt.Errorf("lock heartbeat: %w", err)
	}

	result, err := fn(heartbeatTx{tx: tx})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit heartbeat tx: %w", err)
	}
	return result, nil
}

type heartbeatTx struct {
	tx pgx.Tx
}

func (t heartbeatTx) FindHeartbeat(ctx context.Context, key domain.HeartbeatKey) (*domain.StoredHeartbeat, error) {
	var stored domain.StoredHeartbeat
	err := t.tx.QueryRow(ctx,
		`SELECT sent_at FROM sms_heartbeats WHERE heartbeat_key = $1`, key).Scan(&stored.SentAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find heartbeat: %w", err)
	}
	return &stored, nil
}

func (t heartbeatTx) InsertHeartbeat(ctx context.Context, heartbeat domain.GatewayHeartbeat, contactAt time.Time) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO sms_heartbeats(heartbeat_key, site_id, gateway_id, sequence, sent_at, contact_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		heartbeat.Key(), heartbeat.SiteID, heartbeat.GatewayID, heartbeat.Sequence, heartbeat.SentAt, contactAt)
	if err != nil {
		return fmt.Errorf("insert heartbeat: %w", err)
	}
	return nil
}

func (t heartbeatTx) InsertHeartbeatReceipt(ctx context.Context, key domain.HeartbeatKey, receipt domain.SMSReceipt, pricing domain.SMSPricing) (bool, error) {
	result, err := t.tx.Exec(ctx, `
		INSERT INTO sms_heartbeat_receipts(provider_message_id, heartbeat_key, sender, recipient, received_at, outbound_cost_minor, inbound_cost_minor, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (provider_message_id) DO NOTHING`,
		receipt.ProviderMessageID, key, receipt.Sender, receipt.Recipient, receipt.ReceivedAt,
		pricing.OutboundCostMinor, pricing.InboundCostMinor, pricing.Currency)
	if err != nil {
		return false, fmt.Errorf("insert heartbeat SMS receipt: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func (t heartbeatTx) FindHeartbeatReceiptKey(ctx context.Context, providerMessageID string) (domain.HeartbeatKey, error) {
	var key domain.HeartbeatKey
	err := t.tx.QueryRow(ctx,
		`SELECT heartbeat_key FROM sms_heartbeat_receipts WHERE provider_message_id = $1`,
		providerMessageID).Scan(&key)
	if err != nil {
		return "", fmt.Errorf("find heartbeat receipt key: %w", err)
	}
	return key, nil
}

func (t heartbeatTx) GetMonthlySMSUsage(ctx context.Context, at time.Time, rentalMinor int64) (int64, error) {
	return monthlySMSUsage(ctx, t.tx, at, rentalMinor)
}
