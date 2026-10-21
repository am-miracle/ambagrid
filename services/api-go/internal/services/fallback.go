package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"api-go/internal/domain"
	"api-go/internal/repository"
)

type FallbackService struct {
	repository        repository.FallbackRepository
	topic             string
	pricing           domain.SMSPricing
	budgetWarnedMonth atomic.Int64
}

func NewFallbackService(repository repository.FallbackRepository, topic string, pricing domain.SMSPricing) *FallbackService {
	return &FallbackService{repository: repository, topic: topic, pricing: pricing}
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
	return s.checkBudgetThreshold(ctx, tx, receipt)
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

func (s *FallbackService) checkBudgetThreshold(ctx context.Context, tx repository.FallbackTx, receipt domain.SMSReceipt) error {
	if s.pricing.MonthlyBudgetMinor <= 0 {
		return nil
	}
	usage, err := tx.GetMonthlySMSUsage(ctx, receipt.ReceivedAt, s.pricing.NumberRentalMinor)
	if err != nil {
		return fmt.Errorf("read SMS fallback budget usage: %w", err)
	}
	month := int64(receipt.ReceivedAt.Year())*100 + int64(receipt.ReceivedAt.Month())
	if usage*100 >= s.pricing.MonthlyBudgetMinor*80 && s.budgetWarnedMonth.Swap(month) != month {
		slog.Warn("SMS fallback monthly budget threshold reached",
			"usage_minor", usage, "budget_minor", s.pricing.MonthlyBudgetMinor, "currency", s.pricing.Currency)
	}
	return nil
}
