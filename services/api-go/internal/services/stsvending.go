package services

import (
	"api-go/internal/domain"
	"context"
	"errors"
	"fmt"
	"time"
)

// STSVendStore reserves a reference durably before the first provider call.
// Existing unresolved reservations must never be retried automatically.
type STSVendStore interface {
	Open(context.Context, string) (STSVendSession, error)
}
type STSVendSession interface {
	Reserve(context.Context, domain.STSVendRequest) (*domain.STSVend, bool, error)
	Complete(context.Context, string, domain.STSVend) error
	RecordUnsent(context.Context, string, string) error
	Close()
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
	if err := request.ValidateFunding(); err != nil {
		return domain.STSVend{}, err
	}
	session, err := s.store.Open(ctx, request.Reference)
	if err != nil {
		return domain.STSVend{}, err
	}
	defer session.Close()
	saved, fresh, err := session.Reserve(ctx, request)
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
		if errors.Is(err, domain.ErrVendNotSent) {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if saveErr := session.RecordUnsent(saveCtx, request.Reference, err.Error()); saveErr != nil {
				return domain.STSVend{}, fmt.Errorf("%w: record unsent outcome: %w", domain.ErrVendUncertain, saveErr)
			}
			return domain.STSVend{}, err
		}
		return domain.STSVend{}, fmt.Errorf("%w: %w", domain.ErrVendUncertain, err)
	}
	// Persist a successful reply even if the caller's deadline just elapsed.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := session.Complete(saveCtx, request.Reference, result); err != nil {
		return result, fmt.Errorf("%w: persist vending result: %w", domain.ErrVendUncertain, err)
	}
	return result, nil
}

// Reconciliation is an operator action backed by provider evidence, never an
// automatic retry or an inferred result from a duplicate HTTP error.
type STSReconciliationStore interface {
	Reconcile(context.Context, domain.STSReconciliation) error
}

func ReconcileSTSVend(ctx context.Context, store STSReconciliationStore, r domain.STSReconciliation) error {
	if err := r.Validate(); err != nil {
		return err
	}
	return store.Reconcile(ctx, r)
}

// STSVendReportStore reads reservations and reversals without contacting the provider.
type STSVendReportStore interface {
	ListUnresolved(context.Context) ([]domain.STSVendRecord, error)
	ListReversalShortfalls(context.Context) ([]domain.STSReversalShortfall, error)
}

func ListUnresolvedSTSVends(ctx context.Context, store STSVendReportStore) ([]domain.STSVendRecord, error) {
	records, err := store.ListUnresolved(ctx)
	if err != nil {
		return nil, err
	}
	for i := range records {
		r := &records[i]
		switch {
		case r.NeedsBalanceReversal:
			r.Status = domain.STSVendNeedsBalanceReversal
		case r.SafeToRetry && r.RetryBlockedReason != nil:
			r.Status = domain.STSVendRetryBlocked
		case r.SafeToRetry:
			r.Status = domain.STSVendRetryableUnsent
		default:
			r.Status = domain.STSVendOutcomeUncertain
		}
	}
	return records, nil
}

func ListSTSReversalShortfalls(ctx context.Context, store STSVendReportStore) ([]domain.STSReversalShortfall, error) {
	return store.ListReversalShortfalls(ctx)
}
