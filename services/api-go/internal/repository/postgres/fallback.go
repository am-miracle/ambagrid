package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"api-go/internal/domain"
)

type FallbackStore struct {
	pool    databasePool
	topic   string
	pricing domain.SMSPricing
}

func NewFallbackStore(pool databasePool, topic string, pricing domain.SMSPricing) *FallbackStore {
	return &FallbackStore{pool: pool, topic: topic, pricing: pricing}
}

func (s *FallbackStore) ReceiveSMS(ctx context.Context, event domain.CriticalFallbackEvent, receipt domain.SMSReceipt) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin fallback alert: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, event.EventKey()); err != nil {
		return false, fmt.Errorf("lock fallback event: %w", err)
	}

	var alertID, storedSite, storedAsset, storedCode string
	var storedSequence uint64
	err = tx.QueryRow(ctx, `SELECT alert_id::text, site_id, asset_id, code, sequence FROM sms_fallback_events WHERE event_key = $1 FOR UPDATE`, event.EventKey()).Scan(&alertID, &storedSite, &storedAsset, &storedCode, &storedSequence)
	if err == nil {
		if storedSite != event.SiteID || storedAsset != event.AssetID || storedCode != event.Code || storedSequence != event.Sequence {
			return false, domain.ErrConflict
		}
		if err := s.insertReceipt(ctx, tx, event.EventKey(), receipt); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit duplicate fallback alert: %w", err)
		}
		return false, nil
	}
	if err != pgx.ErrNoRows {
		return false, fmt.Errorf("find fallback alert: %w", err)
	}
	if event.AssetID != "site" {
		var assetType string
		if err := tx.QueryRow(ctx, `SELECT asset_type FROM assets WHERE asset_id = $1 AND site_id = $2`, event.AssetID, event.SiteID).Scan(&assetType); err != nil {
			if err != pgx.ErrNoRows {
				return false, fmt.Errorf("validate fallback asset: %w", err)
			}
			return false, fmt.Errorf("%w: fallback asset %q does not belong to site %q", domain.ErrInvalidID, event.AssetID, event.SiteID)
		}
		if !criticalCodeMatchesAsset(event.Code, assetType) {
			return false, fmt.Errorf("%w: fallback code %q does not apply to asset type %q", domain.ErrInvalidID, event.Code, assetType)
		}
	}

	kind, reason := fallbackAlertDetails(event)
	err = tx.QueryRow(ctx, `SELECT alert_id::text FROM alerts WHERE source_event_id = $1 FOR UPDATE`, event.EventKey()).Scan(&alertID)
	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, `SELECT alert_id::text FROM alerts WHERE site_id = $1 AND asset_id = $2 AND kind = $3 AND status = 'open' FOR UPDATE`, event.SiteID, event.AssetID, kind).Scan(&alertID)
	}
	created := false
	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, `
			INSERT INTO alerts(asset_id, site_id, kind, severity, status, reason, opened_at, source_event_id)
			VALUES ($1, $2, $3, 'critical', 'open', $4, $5, $6)
			RETURNING alert_id::text`, event.AssetID, event.SiteID, kind, reason, event.OccurredAt, event.EventKey()).Scan(&alertID)
		if err != nil {
			return false, fmt.Errorf("insert fallback alert: %w", err)
		}
		created = true
		payload, _ := json.Marshal(map[string]any{
			"alert_id": alertID, "asset_id": event.AssetID, "site_id": event.SiteID,
			"severity": "SEVERITY_CRITICAL", "reason": reason, "opened_at_utc": event.OccurredAt.Unix(),
			"source_event_id": event.EventKey(),
		})
		if _, err := tx.Exec(ctx, `INSERT INTO command_event_outbox(topic, event_type, aggregate_type, aggregate_id, payload) VALUES ($1, 'alert.opened', 'alert', $2, $3::jsonb)`, s.topic, alertID, payload); err != nil {
			return false, fmt.Errorf("insert fallback alert outbox: %w", err)
		}
	} else if err != nil {
		return false, fmt.Errorf("find fallback alert: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sms_fallback_events(event_key, site_id, sequence, asset_id, code, event_at, alert_id) VALUES ($1, $2, $3, $4, $5, $6, $7)`, event.EventKey(), event.SiteID, event.Sequence, event.AssetID, event.Code, event.OccurredAt, alertID); err != nil {
		return false, fmt.Errorf("insert fallback event: %w", err)
	}
	if err := s.insertReceipt(ctx, tx, event.EventKey(), receipt); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit fallback alert: %w", err)
	}
	return created, nil
}

func criticalCodeMatchesAsset(code, assetType string) bool {
	switch code {
	case "BATTERY_OVERHEAT":
		return assetType == "battery_bms"
	case "INVERTER_FAILURE":
		return assetType == "solar_inverter"
	default:
		return true
	}
}

func (s *FallbackStore) insertReceipt(ctx context.Context, tx pgx.Tx, eventKey string, receipt domain.SMSReceipt) error {
	result, err := tx.Exec(ctx, `
		INSERT INTO sms_fallback_receipts(provider_message_id, event_key, gateway_id, sender, recipient, received_at, outbound_cost_minor, inbound_cost_minor, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (provider_message_id) DO NOTHING`,
		receipt.ProviderMessageID, eventKey, receipt.GatewayID, receipt.Sender, receipt.Recipient, receipt.ReceivedAt,
		s.pricing.OutboundCostMinor, s.pricing.InboundCostMinor, s.pricing.Currency)
	if err != nil {
		return fmt.Errorf("insert fallback SMS receipt: %w", err)
	}
	if result.RowsAffected() == 0 {
		var storedEventKey string
		if err := tx.QueryRow(ctx, `SELECT event_key FROM sms_fallback_receipts WHERE provider_message_id = $1`, receipt.ProviderMessageID).Scan(&storedEventKey); err != nil {
			return fmt.Errorf("read duplicate fallback SMS receipt: %w", err)
		}
		if storedEventKey != eventKey {
			return domain.ErrConflict
		}
	}
	if result.RowsAffected() > 0 && s.pricing.MonthlyBudgetMinor > 0 {
		var usage int64
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(outbound_cost_minor + inbound_cost_minor), 0) + $2
			FROM sms_fallback_receipts
			WHERE received_at >= date_trunc('month', $1::timestamptz)
			  AND received_at < date_trunc('month', $1::timestamptz) + interval '1 month'`, receipt.ReceivedAt, s.pricing.NumberRentalMinor).Scan(&usage)
		if err != nil {
			return fmt.Errorf("read fallback SMS monthly cost: %w", err)
		}
		if usage*100 >= s.pricing.MonthlyBudgetMinor*80 {
			slog.Warn("SMS fallback monthly budget threshold reached", "usage_minor", usage, "budget_minor", s.pricing.MonthlyBudgetMinor, "currency", s.pricing.Currency)
		}
	}
	return nil
}

func fallbackAlertDetails(event domain.CriticalFallbackEvent) (string, string) {
	switch event.Code {
	case "BATTERY_OVERHEAT":
		return "battery_overheat", fmt.Sprintf("Battery temperature %.1f C exceeded the critical limit", *event.TemperatureC)
	case "INVERTER_FAILURE":
		return "inverter_failure", "Inverter reported a critical failure"
	case "TAMPER_DETECTED":
		return "tamper_detected", "Asset tamper alarm triggered"
	default:
		return "site_outage", "Full-site outage detected"
	}
}
