package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"api-go/internal/domain"
)

type FallbackService struct {
	repository FallbackRepository
	topic      string
	pricing    domain.SMSPricing
}

func NewFallbackService(repository FallbackRepository, topic string, pricing domain.SMSPricing) *FallbackService {
	return &FallbackService{repository: repository, topic: topic, pricing: pricing}
}

func (s *FallbackService) ReceiveSMS(ctx context.Context, event domain.CriticalFallbackEvent, receipt domain.SMSReceipt) (bool, error) {
	return s.repository.RunFallbackTx(ctx, event.EventKey(), func(tx FallbackTx) (bool, error) {
		stored, err := tx.FindFallbackEvent(ctx, event.EventKey())
		if err != nil {
			return false, err
		}
		if stored != nil {
			if stored.SiteID != event.SiteID || stored.AssetID != event.AssetID || stored.Code != event.Code || stored.Sequence != event.Sequence {
				return false, domain.ErrConflict
			}
			if _, err := tx.InsertReceipt(ctx, event.EventKey(), receipt, s.pricing); err != nil {
				return false, err
			}
			return false, nil
		}

		if event.AssetID != "site" {
			assetType, err := tx.LookupAssetType(ctx, event.AssetID, event.SiteID)
			if err != nil {
				return false, fmt.Errorf("%w: fallback asset %q does not belong to site %q", domain.ErrInvalidID, event.AssetID, event.SiteID)
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
		if err != nil {
			alertID, err = tx.FindOpenAlert(ctx, event.SiteID, event.AssetID, kind)
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

			payload, _ := json.Marshal(map[string]any{
				"alert_id": alertID, "asset_id": event.AssetID, "site_id": event.SiteID,
				"severity": "SEVERITY_CRITICAL", "reason": reason, "opened_at_utc": event.OccurredAt.Unix(),
				"source_event_id": event.EventKey(),
			})
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

		inserted, err := tx.InsertReceipt(ctx, event.EventKey(), receipt, s.pricing)
		if err != nil {
			return false, err
		}

		if !inserted {
			if err := s.checkReceiptConflict(ctx, tx, event.EventKey(), receipt); err != nil {
				return false, err
			}
		} else {
			s.checkBudgetThreshold(ctx, tx, receipt)
		}

		return created, nil
	})
}

func (s *FallbackService) checkReceiptConflict(ctx context.Context, tx FallbackTx, eventKey string, receipt domain.SMSReceipt) error {
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

func (s *FallbackService) checkBudgetThreshold(ctx context.Context, tx FallbackTx, receipt domain.SMSReceipt) {
	if s.pricing.MonthlyBudgetMinor <= 0 {
		return
	}
	usage, err := tx.GetMonthlySMSUsage(ctx, receipt.ReceivedAt, s.pricing.NumberRentalMinor)
	if err != nil {
		return
	}
	if usage*100 >= s.pricing.MonthlyBudgetMinor*80 {
		slog.Warn("SMS fallback monthly budget threshold reached",
			"usage_minor", usage, "budget_minor", s.pricing.MonthlyBudgetMinor, "currency", s.pricing.Currency)
	}
}
