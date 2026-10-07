package services

import (
	"context"

	"api-go/internal/domain"
)

type FallbackService struct{ repository FallbackRepository }

func NewFallbackService(repository FallbackRepository) *FallbackService {
	return &FallbackService{repository: repository}
}

func (s *FallbackService) ReceiveSMS(ctx context.Context, event domain.CriticalFallbackEvent, receipt domain.SMSReceipt) (bool, error) {
	return s.repository.ReceiveSMS(ctx, event, receipt)
}
