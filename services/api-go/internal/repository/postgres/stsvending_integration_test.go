package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/services"
)

type vendingProvider struct {
	calls atomic.Int32
	fail  bool
}

func (v *vendingProvider) Vend(ctx context.Context, r domain.STSVendRequest) (domain.STSVend, error) {
	v.calls.Add(1)
	if v.fail {
		return domain.STSVend{}, context.DeadlineExceeded
	}
	return domain.STSVend{Tokens: []string{"11111111111111111111", "22222222222222222222"}, Units: 12.5, UnitName: "kWh", VendedAt: time.Now().UTC()}, nil
}
func TestSTSVendDurabilityAndConcurrentReservation(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	reference := fmt.Sprintf("test-vend-%d", time.Now().UnixNano())
	t.Cleanup(func() { cleaner(t, pool)("DELETE FROM sts_vends WHERE reference=$1", reference) })
	request := domain.STSVendRequest{Reference: reference, MeterID: "12345678901", AmountMinor: 5001}
	provider := &vendingProvider{}
	service := services.NewSTSVendingService(NewSTSVendStore(pool, "test-installation"), provider)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := service.Vend(ctx, request)
			if err != nil && !errors.Is(err, domain.ErrVendUncertain) {
				t.Errorf("concurrent vend: %v", err)
			}
		})
	}
	wg.Wait()
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("provider called %d times", calls)
	}
	// A new service/store must return the durable result without vending again.
	restarted := services.NewSTSVendingService(NewSTSVendStore(pool, "test-installation"), provider)
	result, err := restarted.Vend(ctx, request)
	if err != nil || len(result.Tokens) != 2 || result.Tokens[1] != "22222222222222222222" || provider.calls.Load() != 1 {
		t.Fatalf("replay=%+v err=%v calls=%d", result, err, provider.calls.Load())
	}
	request.AmountMinor++
	if _, err := restarted.Vend(ctx, request); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mismatched request error=%v", err)
	}
	request.AmountMinor--
	other := services.NewSTSVendingService(NewSTSVendStore(pool, "other-installation"), provider)
	if _, err := other.Vend(ctx, request); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("different installation error=%v", err)
	}
}
func TestSTSVendTimeoutNeverIssuesAgain(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	reference := fmt.Sprintf("test-vend-%d", time.Now().UnixNano())
	t.Cleanup(func() { cleaner(t, pool)("DELETE FROM sts_vends WHERE reference=$1", reference) })
	request := domain.STSVendRequest{Reference: reference, MeterID: "12345678901", AmountMinor: 5001}
	provider := &vendingProvider{fail: true}
	service := services.NewSTSVendingService(NewSTSVendStore(pool, "test-installation"), provider)
	for range 2 {
		if _, err := service.Vend(ctx, request); !errors.Is(err, domain.ErrVendUncertain) {
			t.Fatalf("error=%v", err)
		}
	}
	if provider.calls.Load() != 1 {
		t.Fatalf("ambiguous vend retried %d times", provider.calls.Load())
	}
}
