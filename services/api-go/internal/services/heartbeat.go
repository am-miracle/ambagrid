package services

import (
	"context"
	"fmt"

	"api-go/internal/domain"
	"api-go/internal/repository"
)

type HeartbeatService struct {
	repository repository.HeartbeatRepository
	pricing    domain.SMSPricing
	budget     *smsBudgetMonitor
}

func (s *HeartbeatService) ReceiveHeartbeat(ctx context.Context, heartbeat domain.GatewayHeartbeat, receipt domain.SMSReceipt) (bool, error) {
	key := heartbeat.Key()
	return s.repository.RunHeartbeatTx(ctx, key, func(tx repository.HeartbeatTx) (bool, error) {
		stored, err := tx.FindHeartbeat(ctx, key)
		if err != nil {
			return false, err
		}
		if stored != nil {
			if !stored.Matches(heartbeat) {
				return false, domain.ErrConflict
			}
			return false, s.recordReceipt(ctx, tx, key, receipt)
		}
		if err := tx.InsertHeartbeat(ctx, heartbeat, heartbeat.ContactAt(receipt.ReceivedAt)); err != nil {
			return false, fmt.Errorf("insert gateway heartbeat: %w", err)
		}
		if err := s.recordReceipt(ctx, tx, key, receipt); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (s *HeartbeatService) recordReceipt(ctx context.Context, tx repository.HeartbeatTx, key domain.HeartbeatKey, receipt domain.SMSReceipt) error {
	inserted, err := tx.InsertHeartbeatReceipt(ctx, key, receipt, s.pricing)
	if err != nil {
		return err
	}
	if !inserted {
		storedKey, err := tx.FindHeartbeatReceiptKey(ctx, receipt.ProviderMessageID)
		if err != nil {
			return fmt.Errorf("check heartbeat receipt conflict: %w", err)
		}
		if storedKey != key {
			return domain.ErrConflict
		}
		return nil
	}
	return s.budget.check(ctx, tx, receipt.ReceivedAt)
}
