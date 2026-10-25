package services

import (
	"api-go/internal/domain"
	"context"
	"fmt"
	"time"
)

// STSVendStore reserves a reference durably before the first provider call.
// Existing unresolved reservations must never be retried automatically.
type STSVendStore interface {
	Reserve(context.Context, domain.STSVendRequest) (saved *domain.STSVend, fresh bool, err error)
	Complete(context.Context, string, domain.STSVend) error
}
type STSVendor interface {
	Vend(context.Context, domain.STSVendRequest) (domain.STSVend, error)
}
type STSVendingService struct {
	store  STSVendStore
	vendor STSVendor
}

func NewSTSVendingService(store STSVendStore, vendor STSVendor) *STSVendingService {
	return &STSVendingService{store: store, vendor: vendor}
}
func (s *STSVendingService) Vend(ctx context.Context, request domain.STSVendRequest) (domain.STSVend, error) {
	if err := request.Validate(); err != nil {
		return domain.STSVend{}, err
	}
	saved, fresh, err := s.store.Reserve(ctx, request)
	if err != nil {
		return domain.STSVend{}, err
	}
	if saved != nil {
		return *saved, nil
	}
	if !fresh {
		return domain.STSVend{}, domain.ErrVendUncertain
	}
	result, err := s.vendor.Vend(ctx, request)
	if err != nil {
		return domain.STSVend{}, fmt.Errorf("%w: %w", domain.ErrVendUncertain, err)
	}
	// Persist a successful reply even if the caller's deadline just elapsed.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.Complete(saveCtx, request.Reference, result); err != nil {
		return domain.STSVend{}, fmt.Errorf("%w: persist vending result: %w", domain.ErrVendUncertain, err)
	}
	return result, nil
}
