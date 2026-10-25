package postgres

import (
	"context"
	"errors"

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
	return domain.STSVend{Tokens: []string{"11111111111111111111", "22222222222222222222"}, Units: 12.5, UnitName: "kWh", VendedAt: time.Now().UTC(), IDRecord: "12345678901234567890123456789012345"}, nil
}
func TestSTSVendDurabilityAndConcurrentReservation(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	pool := f.pool
	ctx := context.Background()
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
	f, request, _ := fundedSTSRequest(t, "sts")
	pool := f.pool
	ctx := context.Background()
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

func fundedSTSRequest(t *testing.T, mode string) (edgeFixture, domain.STSVendRequest, domain.ApplyPaymentResult) {
	t.Helper()
	f := newEdgeFixture(t)
	ctx := context.Background()
	customerID, tariffID := f.siteID+"-customer", f.siteID+"-tariff"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE smart_meters SET credit_mode=$2,sts_meter_number='12345678901',sts_installation='test-installation',sts_currency='NGN' WHERE meter_id=$1", f.meterID, mode)
	exec("INSERT INTO customers(customer_id,site_id,display_name)VALUES($1,$2,'STS test')", customerID, f.siteID)
	exec("INSERT INTO meter_assignments(site_id,meter_id,customer_id)VALUES($1,$2,$3)", f.siteID, f.meterID, customerID)
	exec("INSERT INTO tariff_plans(tariff_plan_id,site_id,name,currency,price_per_kwh,effective_from)VALUES($1,$2,'STS test','NGN',250,now()-interval '1 day')", tariffID, f.siteID)
	clean := cleaner(t, f.pool)
	t.Cleanup(func() {
		clean("DELETE FROM sts_vend_reconciliations WHERE reference IN(SELECT reference FROM sts_vends WHERE payment_id IN(SELECT payment_id FROM payments WHERE customer_id=$1))", customerID)
		clean("DELETE FROM sts_vends WHERE payment_id IN(SELECT payment_id FROM payments WHERE customer_id=$1)", customerID)
		clean("DELETE FROM command_event_outbox WHERE aggregate_id IN(SELECT command_id::text FROM meter_commands WHERE meter_id=$1)OR aggregate_id IN(SELECT payment_id::text FROM payments WHERE customer_id=$2)OR aggregate_id IN(SELECT credit_id::text FROM energy_credits WHERE site_id=$3)", f.meterID, customerID, f.siteID)
		clean("DELETE FROM audit_events WHERE site_id=$1", f.siteID)
		clean("DELETE FROM meter_commands WHERE meter_id=$1", f.meterID)
		clean("UPDATE credit_balances SET remaining_kwh=0,remaining_money_value_minor_units=0 WHERE assignment_id IN(SELECT assignment_id FROM meter_assignments WHERE customer_id=$1)", customerID)
		clean("DELETE FROM credit_balances WHERE assignment_id IN(SELECT assignment_id FROM meter_assignments WHERE customer_id=$1)", customerID)
		clean("DELETE FROM energy_credits WHERE site_id=$1", f.siteID)
		clean("DELETE FROM payments WHERE customer_id=$1", customerID)
		clean("DELETE FROM meter_assignments WHERE customer_id=$1", customerID)
		clean("DELETE FROM tariff_plans WHERE tariff_plan_id=$1", tariffID)
		clean("DELETE FROM customers WHERE customer_id=$1", customerID)
	})
	result, err := services.NewPaymentService(NewStore(f.pool, 5*time.Second)).ApplyWebhookPayment(ctx, domain.ApplyPaymentCommand{Provider: "integration", ExternalReference: f.siteID + "-ref", CustomerID: customerID, AmountMinorUnits: 5001, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return f, domain.STSVendRequest{PaymentID: result.Payment.PaymentID, Reference: result.Payment.PaymentID, MeterID: "12345678901", AmountMinor: 5001}, result
}
func TestSTSPaymentCannotAlsoReceiveAutomaticCredit(t *testing.T) {
	f, request, result := fundedSTSRequest(t, "sts")
	if result.MeterCommand != nil {
		t.Fatal("STS payment created automatic command")
	}
	service := services.NewSTSVendingService(NewSTSVendStore(f.pool, "test-installation"), &vendingProvider{})
	if _, err := service.Vend(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	_, err := f.pool.Exec(context.Background(), `INSERT INTO meter_commands(meter_id,command_type,status,requested_by,reason,expires_at,credit_kwh,credit_id)VALUES($1,'credit_meter','requested','test','test',now()+interval '1 hour',1,$2)`, f.meterID, result.Credit.CreditID)
	if err == nil {
		t.Fatal("STS credit also accepted as kWh command")
	}
	request.Reference = "different-reference"
	if _, err := service.Vend(context.Background(), request); err == nil {
		t.Fatal("payment vended under second identity")
	}
}
func TestMeterModePaymentCannotBeVendedAfterModeChange(t *testing.T) {
	f, request, result := fundedSTSRequest(t, "meter")
	if result.MeterCommand == nil {
		t.Fatal("meter payment missing automatic command")
	}
	if _, err := f.pool.Exec(context.Background(), "UPDATE smart_meters SET credit_mode='sts' WHERE meter_id=$1", f.meterID); err != nil {
		t.Fatal(err)
	}
	provider := &vendingProvider{}
	service := services.NewSTSVendingService(NewSTSVendStore(f.pool, "test-installation"), provider)
	if _, err := service.Vend(context.Background(), request); err == nil {
		t.Fatal("automatically credited payment also vended")
	}
	if provider.calls.Load() != 0 {
		t.Fatal("provider contacted for ineligible payment")
	}
}
func TestSTSReconciliationRecordsRecoveredTokensWithoutVending(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	store := NewSTSVendStore(f.pool, "test-installation")
	provider := &vendingProvider{fail: true}
	service := services.NewSTSVendingService(store, provider)
	if _, err := service.Vend(ctx, request); !errors.Is(err, domain.ErrVendUncertain) {
		t.Fatalf("error=%v", err)
	}
	records, err := store.ListUnresolved(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range records {
		if r.Reference == request.Reference {
			found = true
		}
	}
	if !found {
		t.Fatal("uncertain vend not listed")
	}
	result := domain.STSVend{Tokens: []string{"11111111111111111111"}, Units: 2, UnitName: "kWh", VendedAt: time.Now().UTC(), IDRecord: "12345678901234567890123456789012345"}
	reconciliation := domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: "operator:test", Evidence: "provider ticket 42, verified transaction", Result: &result}
	if err := services.ReconcileSTSVend(ctx, store, reconciliation); err != nil {
		t.Fatal(err)
	}
	saved, err := service.Vend(ctx, request)
	if err != nil || len(saved.Tokens) != 1 || provider.calls.Load() != 1 {
		t.Fatalf("saved=%+v err=%v calls=%d", saved, err, provider.calls.Load())
	}
	var count int
	if err := f.pool.QueryRow(ctx, "SELECT count(*)FROM sts_vend_reconciliations WHERE reference=$1", request.Reference).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	if err := services.ReconcileSTSVend(ctx, store, reconciliation); err == nil {
		t.Fatal("overwrote resolved outcome")
	}
}
func TestSTSConfirmedNotIssuedCannotBeRetried(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	store := NewSTSVendStore(f.pool, "test-installation")
	provider := &vendingProvider{fail: true}
	service := services.NewSTSVendingService(store, provider)
	service.Vend(ctx, request)
	if err := services.ReconcileSTSVend(ctx, store, domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: "operator:test", Evidence: "provider confirms no issuance, ticket 43", NotIssued: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Vend(ctx, request); !errors.Is(err, domain.ErrVendNotIssued) {
		t.Fatalf("error=%v", err)
	}
	if provider.calls.Load() != 1 {
		t.Fatal("provider contacted again")
	}
}

type pausedVendor struct {
	entered chan struct{}
	release chan struct{}
}

func (v pausedVendor) Vend(ctx context.Context, r domain.STSVendRequest) (domain.STSVend, error) {
	close(v.entered)
	select {
	case <-v.release:
	case <-ctx.Done():
		return domain.STSVend{}, ctx.Err()
	}
	return (&vendingProvider{}).Vend(ctx, r)
}
func TestReconciliationCannotCloseAnActiveVend(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	store := NewSTSVendStore(f.pool, "test-installation")
	provider := pausedVendor{entered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service := services.NewSTSVendingService(store, provider)
	done := make(chan error, 1)
	go func() { _, err := service.Vend(ctx, request); done <- err }()
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("provider never entered")
	}
	err := services.ReconcileSTSVend(ctx, store, domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: "operator:test", Evidence: "test evidence", NotIssued: true})
	close(provider.release)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("active reservation reconciled: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("provider result lost: %v", err)
	}
	saved, err := service.Vend(ctx, request)
	if err != nil || len(saved.Tokens) != 2 {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
}
func TestPaymentLocksDeliveryModeUntilTransactionEnds(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	var customerID string
	if err := f.pool.QueryRow(context.Background(), "SELECT customer_id FROM payments WHERE payment_id=$1", request.PaymentID).Scan(&customerID); err != nil {
		t.Fatal(err)
	}
	store := NewStore(f.pool, 5*time.Second)
	_, err := store.RunPaymentTx(context.Background(), func(tx services.PaymentTx) (domain.ApplyPaymentResult, error) {
		if _, err := tx.FindActiveAssignment(context.Background(), customerID); err != nil {
			return domain.ApplyPaymentResult{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := f.pool.Exec(ctx, "UPDATE smart_meters SET credit_mode='meter' WHERE meter_id=$1", f.meterID)
		if err == nil {
			t.Fatal("delivery mode changed while payment transaction holds snapshot")
		}
		return domain.ApplyPaymentResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
