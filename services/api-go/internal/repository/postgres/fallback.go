package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"api-go/internal/domain"
	"api-go/internal/services"
)

type FallbackStore struct {
	pool databasePool
}

func NewFallbackStore(pool databasePool) *FallbackStore {
	return &FallbackStore{pool: pool}
}

func (s *FallbackStore) RunFallbackTx(ctx context.Context, eventKey string, fn func(services.FallbackTx) (bool, error)) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin fallback tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, eventKey); err != nil {
		return false, fmt.Errorf("lock fallback event: %w", err)
	}

	result, err := fn(fallbackTx{tx: tx})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit fallback tx: %w", err)
	}
	return result, nil
}

type fallbackTx struct {
	tx pgx.Tx
}

func (t fallbackTx) FindFallbackEvent(ctx context.Context, eventKey string) (*domain.StoredFallbackEvent, error) {
	var e domain.StoredFallbackEvent
	err := t.tx.QueryRow(ctx,
		`SELECT alert_id::text, site_id, asset_id, code, sequence FROM sms_fallback_events WHERE event_key = $1 FOR UPDATE`,
		eventKey).Scan(&e.AlertID, &e.SiteID, &e.AssetID, &e.Code, &e.Sequence)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find fallback event: %w", err)
	}
	return &e, nil
}

func (t fallbackTx) LookupAssetType(ctx context.Context, assetID, siteID string) (string, error) {
	var assetType string
	err := t.tx.QueryRow(ctx,
		`SELECT asset_type FROM assets WHERE asset_id = $1 AND site_id = $2`,
		assetID, siteID).Scan(&assetType)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("asset not found")
	}
	if err != nil {
		return "", fmt.Errorf("lookup asset type: %w", err)
	}
	return assetType, nil
}

func (t fallbackTx) FindAlertBySourceEvent(ctx context.Context, sourceEventID string) (string, error) {
	var alertID string
	err := t.tx.QueryRow(ctx,
		`SELECT alert_id::text FROM alerts WHERE source_event_id = $1 FOR UPDATE`,
		sourceEventID).Scan(&alertID)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("alert not found")
	}
	if err != nil {
		return "", fmt.Errorf("find alert by source event: %w", err)
	}
	return alertID, nil
}

func (t fallbackTx) FindOpenAlert(ctx context.Context, siteID, assetID, kind string) (string, error) {
	var alertID string
	err := t.tx.QueryRow(ctx,
		`SELECT alert_id::text FROM alerts WHERE site_id = $1 AND asset_id = $2 AND kind = $3 AND status = 'open' FOR UPDATE`,
		siteID, assetID, kind).Scan(&alertID)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("open alert not found")
	}
	if err != nil {
		return "", fmt.Errorf("find open alert: %w", err)
	}
	return alertID, nil
}

func (t fallbackTx) CreateAlert(ctx context.Context, cmd domain.CreateAlertCommand) (string, error) {
	var alertID string
	err := t.tx.QueryRow(ctx, `
		INSERT INTO alerts(asset_id, site_id, kind, severity, status, reason, opened_at, source_event_id)
		VALUES ($1, $2, $3, 'critical', 'open', $4, $5, $6)
		RETURNING alert_id::text`,
		cmd.AssetID, cmd.SiteID, cmd.Kind, cmd.Reason, cmd.OccurredAt, cmd.SourceEventID).Scan(&alertID)
	if err != nil {
		return "", fmt.Errorf("create alert: %w", err)
	}
	return alertID, nil
}

func (t fallbackTx) PublishOutboxEvent(ctx context.Context, topic, eventType, aggregateType, aggregateID string, payload []byte) error {
	_, err := t.tx.Exec(ctx,
		`INSERT INTO command_event_outbox(topic, event_type, aggregate_type, aggregate_id, payload) VALUES ($1, $2, $3, $4, $5::jsonb)`,
		topic, eventType, aggregateType, aggregateID, payload)
	if err != nil {
		return fmt.Errorf("publish outbox event: %w", err)
	}
	return nil
}

func (t fallbackTx) InsertFallbackEvent(ctx context.Context, cmd domain.InsertFallbackEventCommand) error {
	_, err := t.tx.Exec(ctx,
		`INSERT INTO sms_fallback_events(event_key, site_id, sequence, asset_id, code, event_at, alert_id) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		cmd.EventKey, cmd.SiteID, cmd.Sequence, cmd.AssetID, cmd.Code, cmd.EventAt, cmd.AlertID)
	if err != nil {
		return fmt.Errorf("insert fallback event: %w", err)
	}
	return nil
}

func (t fallbackTx) InsertReceipt(ctx context.Context, eventKey string, receipt domain.SMSReceipt, pricing domain.SMSPricing) (bool, error) {
	result, err := t.tx.Exec(ctx, `
		INSERT INTO sms_fallback_receipts(provider_message_id, event_key, gateway_id, sender, recipient, received_at, outbound_cost_minor, inbound_cost_minor, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (provider_message_id) DO NOTHING`,
		receipt.ProviderMessageID, eventKey, receipt.GatewayID, receipt.Sender, receipt.Recipient, receipt.ReceivedAt,
		pricing.OutboundCostMinor, pricing.InboundCostMinor, pricing.Currency)
	if err != nil {
		return false, fmt.Errorf("insert fallback SMS receipt: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func (t fallbackTx) FindReceiptEventKey(ctx context.Context, providerMessageID string) (string, error) {
	var eventKey string
	err := t.tx.QueryRow(ctx,
		`SELECT event_key FROM sms_fallback_receipts WHERE provider_message_id = $1`,
		providerMessageID).Scan(&eventKey)
	if err != nil {
		return "", fmt.Errorf("find receipt event key: %w", err)
	}
	return eventKey, nil
}

func (t fallbackTx) GetMonthlySMSUsage(ctx context.Context, at time.Time, rentalMinor int64) (int64, error) {
	var usage int64
	err := t.tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(outbound_cost_minor + inbound_cost_minor), 0) + $2
		FROM sms_fallback_receipts
		WHERE received_at >= date_trunc('month', $1::timestamptz)
		  AND received_at < date_trunc('month', $1::timestamptz) + interval '1 month'`,
		at, rentalMinor).Scan(&usage)
	if err != nil {
		return 0, fmt.Errorf("read fallback SMS monthly cost: %w", err)
	}
	return usage, nil
}
