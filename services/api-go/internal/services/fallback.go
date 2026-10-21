package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"api-go/internal/domain"
	"api-go/internal/repository"
)

type FallbackService struct {
	repository repository.FallbackRepository
	topic      string
	pricing    domain.SMSPricing
	budget     *smsBudgetMonitor
}

func NewFallbackService(repository repository.FallbackRepository, topic string, pricing domain.SMSPricing) *FallbackService {
	return &FallbackService{repository: repository, topic: topic, pricing: pricing, budget: &smsBudgetMonitor{pricing: pricing}}
}

// Heartbeats shares pricing and budget warnings with critical fallback so the
// monthly threshold reflects every inbound SMS.
func (s *FallbackService) Heartbeats(repository repository.HeartbeatRepository) *HeartbeatService {
	return &HeartbeatService{repository: repository, pricing: s.pricing, budget: s.budget}
}

func (s *FallbackService) ReceiveSMS(ctx context.Context, event domain.CriticalFallbackEvent, receipt domain.SMSReceipt) (bool, error) {
	return s.repository.RunFallbackTx(ctx, event.EventKey(), func(tx repository.FallbackTx) (bool, error) {
		stored, err := tx.FindFallbackEvent(ctx, event.EventKey())
		if err != nil {
			return false, err
		}
		if stored != nil {
			if !stored.Matches(event) {
				return false, domain.ErrConflict
			}
			if err := s.recordReceipt(ctx, tx, event.EventKey(), receipt); err != nil {
				return false, err
			}
			return false, nil
		}

		if event.AssetID != "site" {
			assetType, err := tx.LookupAssetType(ctx, event.AssetID, event.SiteID)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return false, fmt.Errorf("%w: fallback asset %q does not belong to site %q", domain.ErrInvalidID, event.AssetID, event.SiteID)
				}
				return false, fmt.Errorf("lookup fallback asset: %w", err)
			}
			if !event.Code.MatchesAsset(assetType) {
				return false, fmt.Errorf("%w: fallback code %q does not apply to asset type %q", domain.ErrInvalidID, event.Code, assetType)
			}
		}

		kind, reason, err := event.AlertDetails()
		if err != nil {
			return false, fmt.Errorf("%w: %v", domain.ErrInvalidID, err)
		}

		alertID, err := tx.FindAlertBySourceEvent(ctx, event.EventKey())
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return false, fmt.Errorf("find fallback alert by source event: %w", err)
		}
		if errors.Is(err, domain.ErrNotFound) {
			alertID, err = tx.FindOpenAlert(ctx, event.SiteID, event.AssetID, kind)
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return false, fmt.Errorf("find open fallback alert: %w", err)
			}
		}

		created := false
		if err != nil {
			alertID, err = tx.CreateAlert(ctx, domain.CreateAlertCommand{
				AssetID: event.AssetID, SiteID: event.SiteID,
				Kind: kind, Reason: reason,
				OccurredAt: event.OccurredAt, SourceEventID: event.EventKey(),
			})
			if err != nil {
				return false, fmt.Errorf("insert fallback alert: %w", err)
			}
			created = true

			payload, err := json.Marshal(map[string]any{
				"alert_id": alertID, "asset_id": event.AssetID, "site_id": event.SiteID,
				"severity": "SEVERITY_CRITICAL", "reason": reason, "opened_at_utc": event.OccurredAt.Unix(),
				"source_event_id": event.EventKey(),
			})
			if err != nil {
				return false, fmt.Errorf("encode fallback alert outbox: %w", err)
			}
			if err := tx.PublishOutboxEvent(ctx, s.topic, "alert.opened", "alert", alertID, payload); err != nil {
				return false, fmt.Errorf("insert fallback alert outbox: %w", err)
			}
		}

		if err := tx.InsertFallbackEvent(ctx, domain.InsertFallbackEventCommand{
			EventKey: event.EventKey(), SiteID: event.SiteID,
			Sequence: event.Sequence, AssetID: event.AssetID,
			Code: event.Code, EventAt: event.OccurredAt, AlertID: alertID,
		}); err != nil {
			return false, fmt.Errorf("insert fallback event: %w", err)
		}

		if err := s.recordReceipt(ctx, tx, event.EventKey(), receipt); err != nil {
			return false, err
		}

		return created, nil
	})
}

func (s *FallbackService) recordReceipt(ctx context.Context, tx repository.FallbackTx, eventKey domain.EventKey, receipt domain.SMSReceipt) error {
	inserted, err := tx.InsertReceipt(ctx, eventKey, receipt, s.pricing)
	if err != nil {
		return err
	}
	if !inserted {
		return s.checkReceiptConflict(ctx, tx, eventKey, receipt)
	}
	return s.budget.check(ctx, tx, receipt.ReceivedAt)
}

func (s *FallbackService) checkReceiptConflict(ctx context.Context, tx repository.FallbackTx, eventKey domain.EventKey, receipt domain.SMSReceipt) error {
	if receipt.ProviderMessageID == "" {
		return nil
	}
	storedKey, err := tx.FindReceiptEventKey(ctx, receipt.ProviderMessageID)
	if err != nil {
		return fmt.Errorf("check fallback receipt conflict: %w", err)
	}
	if storedKey != eventKey {
		return domain.ErrConflict
	}
	return nil
}

type smsUsageReader interface {
	GetMonthlySMSUsage(ctx context.Context, at time.Time, rentalMinor int64) (int64, error)
}

type smsBudgetMonitor struct {
	pricing     domain.SMSPricing
	warnedMonth atomic.Int64
}

func (m *smsBudgetMonitor) check(ctx context.Context, usageReader smsUsageReader, receivedAt time.Time) error {
	if m.pricing.MonthlyBudgetMinor <= 0 {
		return nil
	}
	usage, err := usageReader.GetMonthlySMSUsage(ctx, receivedAt, m.pricing.NumberRentalMinor)
	if err != nil {
		return fmt.Errorf("read SMS fallback budget usage: %w", err)
	}
	month := int64(receivedAt.Year())*100 + int64(receivedAt.Month())
	if usage*100 >= m.pricing.MonthlyBudgetMinor*80 && m.warnedMonth.Swap(month) != month {
		slog.Warn("SMS fallback monthly budget threshold reached",
			"usage_minor", usage, "budget_minor", m.pricing.MonthlyBudgetMinor, "currency", m.pricing.Currency)
	}
	return nil
}
