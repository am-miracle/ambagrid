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
			if err != nil && !errors.Is(err, domain.ErrVendUncertain) && !errors.Is(err, domain.ErrVendInProgress) {
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
		clean("DELETE FROM sts_credit_reversals WHERE reference IN(SELECT reference FROM sts_vends WHERE payment_id IN(SELECT payment_id FROM payments WHERE customer_id=$1))", customerID)
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

func TestConcurrentVendReportsInProgressRatherThanUncertain(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	provider := pausedVendor{entered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service := services.NewSTSVendingService(NewSTSVendStore(f.pool, "test-installation"), provider)
	done := make(chan error, 1)
	go func() { _, err := service.Vend(ctx, request); done <- err }()
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal("provider never entered")
	}
	_, err := service.Vend(ctx, request)
	close(provider.release)
	if !errors.Is(err, domain.ErrVendInProgress) || errors.Is(err, domain.ErrVendUncertain) {
		t.Fatalf("active vend misclassified: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConfirmedNonIssuanceReversesOnlyItsFundedCredit(t *testing.T) {
	f, request, result := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	store := NewSTSVendStore(f.pool, "test-installation")
	service := services.NewSTSVendingService(store, &vendingProvider{fail: true})
	if _, err := service.Vend(ctx, request); !errors.Is(err, domain.ErrVendUncertain) {
		t.Fatal(err)
	}
	if err := services.ReconcileSTSVend(ctx, store, domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: "operator:test", Evidence: "provider confirms no tokens issued", NotIssued: true}); err != nil {
		t.Fatal(err)
	}
	var kwh float64
	var money int64
	if err := f.pool.QueryRow(ctx, "SELECT remaining_kwh,remaining_money_value_minor_units FROM credit_balances WHERE assignment_id=$1", result.Credit.AssignmentID).Scan(&kwh, &money); err != nil {
		t.Fatal(err)
	}
	if kwh != 0 || money != 0 {
		t.Fatalf("undelivered credit remains in balance: %v kWh, %d minor units", kwh, money)
	}
}

type failBeforeSendOnce struct{ calls int }

func (v *failBeforeSendOnce) Vend(ctx context.Context, r domain.STSVendRequest) (domain.STSVend, error) {
	v.calls++
	if v.calls == 1 {
		return domain.STSVend{}, domain.ErrVendNotSent
	}
	return (&vendingProvider{}).Vend(ctx, r)
}
func TestDefinitelyUnsentVendCanRetryWithSameFunding(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	store := NewSTSVendStore(f.pool, "test-installation")
	provider := &failBeforeSendOnce{}
	service := services.NewSTSVendingService(store, provider)
	if _, err := service.Vend(ctx, request); !errors.Is(err, domain.ErrVendNotSent) || errors.Is(err, domain.ErrVendUncertain) {
		t.Fatalf("unsent error=%v", err)
	}
	r := listedSTSVend(t, store, request.Reference)
	if r.Status != domain.STSVendRetryableUnsent || !r.SafeToRetry || r.RetryBlockedReason != nil || r.LastError == nil {
		t.Fatalf("unsent request must be visible without implying an uncertain outcome: %+v", r)
	}
	result, err := service.Vend(ctx, request)
	if err != nil || len(result.Tokens) != 2 || provider.calls != 2 {
		t.Fatalf("retry=%+v err=%v calls=%d", result, err, provider.calls)
	}
	if _, err := service.Vend(ctx, request); err != nil || provider.calls != 2 {
		t.Fatalf("completed vend repeated: %v", err)
	}
	records, err := services.ListUnresolvedSTSVends(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Reference == request.Reference {
			t.Fatal("completed vend remains in incomplete reservations")
		}
	}
}
func TestRetryRechecksFundingAfterRefund(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	provider := &failBeforeSendOnce{}
	store := NewSTSVendStore(f.pool, "test-installation")
	service := services.NewSTSVendingService(store, provider)
	if _, err := service.Vend(ctx, request); !errors.Is(err, domain.ErrVendNotSent) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE payments SET status='reversed' WHERE payment_id=$1", request.PaymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Vend(ctx, request); err == nil {
		t.Fatal("refunded payment retried")
	}
	if provider.calls != 1 {
		t.Fatal("provider contacted after funding changed")
	}
	r := listedSTSVend(t, store, request.Reference)
	if r.Status != domain.STSVendRetryBlocked || !r.SafeToRetry || r.RetryBlockedReason == nil || *r.RetryBlockedReason != "payment_reversed" || r.LastError == nil {
		t.Fatalf("refunded request is hidden or misclassified: %+v", r)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE payments SET status='confirmed' WHERE payment_id=$1", request.PaymentID); err != nil {
		t.Fatal(err)
	}
	r = listedSTSVend(t, store, request.Reference)
	if r.Status != domain.STSVendRetryableUnsent || r.RetryBlockedReason != nil {
		t.Fatalf("listing retained a stale funding rejection: %+v", r)
	}
	if _, err := service.Vend(ctx, request); err != nil || provider.calls != 2 {
		t.Fatalf("restored funding cannot retry the same payment: calls=%d err=%v", provider.calls, err)
	}
}

func listedSTSVend(t *testing.T, store services.STSVendReportStore, reference string) domain.STSVendRecord {
	t.Helper()
	records, err := services.ListUnresolvedSTSVends(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Reference == reference {
			return record
		}
	}
	t.Fatalf("reservation %s missing from operator listing", reference)
	return domain.STSVendRecord{}
}

func TestUnsentSTSReservationShowsCurrentConfigurationBlock(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	store := NewSTSVendStore(f.pool, "test-installation")
	provider := &failBeforeSendOnce{}
	service := services.NewSTSVendingService(store, provider)
	if _, err := service.Vend(ctx, request); !errors.Is(err, domain.ErrVendNotSent) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE smart_meters SET sts_installation='replacement-installation' WHERE meter_id=$1", f.meterID); err != nil {
		t.Fatal(err)
	}
	r := listedSTSVend(t, store, request.Reference)
	if r.Status != domain.STSVendRetryBlocked || r.RetryBlockedReason == nil || *r.RetryBlockedReason != "installation_changed" {
		t.Fatalf("configuration change not visible before retry: %+v", r)
	}
	if _, err := service.Vend(ctx, request); err == nil || provider.calls != 1 {
		t.Fatalf("blocked reservation retried: calls=%d err=%v", provider.calls, err)
	}
	records, err := NewSTSVendStore(f.pool, "replacement-installation").ListUnresolved(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.Reference == request.Reference {
			t.Fatal("reservation leaked into another installation's operator list")
		}
	}
}
func TestNonIssuancePreservesOtherPaymentsAndNetSummary(t *testing.T) {
	f, request, _ := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	store := NewStore(f.pool, 5*time.Second)
	second, err := services.NewPaymentService(store).ApplyWebhookPayment(ctx, domain.ApplyPaymentCommand{Provider: "integration", ExternalReference: f.siteID + "-second", CustomerID: f.siteID + "-customer", AmountMinorUnits: 7500, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	vends := NewSTSVendStore(f.pool, "test-installation")
	services.NewSTSVendingService(vends, &vendingProvider{fail: true}).Vend(ctx, request)
	reconciliation := domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: "operator:test", Evidence: "provider definitive non-issuance", NotIssued: true}
	if err := services.ReconcileSTSVend(ctx, vends, reconciliation); err != nil {
		t.Fatal(err)
	}
	if err := services.ReconcileSTSVend(ctx, vends, reconciliation); err == nil {
		t.Fatal("same credit reversed twice")
	}
	summaries, err := store.ListCustomerSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, summary := range summaries {
		if summary.CustomerID == second.Payment.CustomerID {
			found = true
			if summary.RemainingKWh != second.Credit.KWhGranted || summary.RemainingMoneyValueMinorUnits != 7500 || summary.TotalKWhPurchased != second.Credit.KWhGranted {
				t.Fatalf("reversal changed other funding: %+v", summary)
			}
		}
	}
	if !found {
		t.Fatal("customer summary missing")
	}
}
func TestInsufficientBalanceReversesPairedPortionAndRecordsShortfall(t *testing.T) {
	f, request, result := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	store := NewSTSVendStore(f.pool, "test-installation")
	services.NewSTSVendingService(store, &vendingProvider{fail: true}).Vend(ctx, request)
	// 5001 minor units at 250 NGN/kWh credited 0.200040 kWh; only 0.1 remains.
	if _, err := f.pool.Exec(ctx, "UPDATE credit_balances SET remaining_kwh=0.1 WHERE assignment_id=$1", result.Credit.AssignmentID); err != nil {
		t.Fatal(err)
	}
	if err := services.ReconcileSTSVend(ctx, store, domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: "operator:test", Evidence: "provider definitive non-issuance", NotIssued: true}); err != nil {
		t.Fatalf("non-issuance blocked by a short balance: %v", err)
	}

	var closed bool
	if err := f.pool.QueryRow(ctx, "SELECT not_issued_at IS NOT NULL FROM sts_vends WHERE reference=$1", request.Reference).Scan(&closed); err != nil || !closed {
		t.Fatalf("non-issuance recorded = %v, %v", closed, err)
	}
	var balanceKWh string
	var balanceMoney int64
	if err := f.pool.QueryRow(ctx, "SELECT remaining_kwh::text,remaining_money_value_minor_units FROM credit_balances WHERE assignment_id=$1", result.Credit.AssignmentID).Scan(&balanceKWh, &balanceMoney); err != nil {
		t.Fatal(err)
	}
	if balanceKWh != "0.000000" || balanceMoney != 2501 {
		t.Fatalf("balance = %s kWh, %d minor; want zero kWh and 2501 minor units", balanceKWh, balanceMoney)
	}
	var reversedKWh, shortfallKWh string
	var reversedMoney, shortfallMoney int64
	if err := f.pool.QueryRow(ctx, "SELECT kwh::text,shortfall_kwh::text,money_minor,shortfall_money_minor FROM sts_credit_reversals WHERE reference=$1", request.Reference).Scan(&reversedKWh, &shortfallKWh, &reversedMoney, &shortfallMoney); err != nil {
		t.Fatal(err)
	}
	if reversedKWh != "0.100000" || shortfallKWh != "0.100040" || reversedMoney != 2500 || shortfallMoney != 2501 {
		t.Fatalf("reversal = %s kWh (shortfall %s), %d minor (shortfall %d)", reversedKWh, shortfallKWh, reversedMoney, shortfallMoney)
	}

	shortfalls, err := store.ListReversalShortfalls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(shortfalls) != 1 || shortfalls[0].Reference != request.Reference || shortfalls[0].ShortfallKWh != "0.100040" || shortfalls[0].ShortfallMoneyMinor != 2501 {
		t.Fatalf("shortfalls = %+v, want this reversal listed for review", shortfalls)
	}
}

func TestSTSReversalUsesOriginalCreditProportionsAndRoundsDown(t *testing.T) {
	for _, tc := range []struct {
		name, creditKWh, balanceKWh, reversedKWh string
		creditMoney, balanceMoney, reversedMoney int64
	}{
		{"money limits reversal", "0.200040", "0.200040", "0.100000", 5001, 2500, 2500},
		{"zero energy preserves money", "0.200040", "0", "0.000000", 5001, 5001, 0},
		{"zero money preserves energy", "0.200040", "0.200040", "0.000000", 5001, 0, 0},
		{"less than one minor unit", "0.200040", "0.000039", "0.000000", 5001, 5001, 0},
		{"fractional micro kWh rounds down", "0.200041", "0.200041", "0.100000", 5001, 2500, 2500},
		{"less than one micro kWh", "0.000001", "0.000001", "0.000000", 5001, 2500, 0},
		{"zero-value legacy credit", "0.200040", "0.100000", "0.100000", 0, 1000, 0},
		{"larger balance caps at original credit", "0.200040", "1", "0.200040", 5001, 10000, 5001},
		{"large money stays exact", "0.200040", "0.100020", "0.100019", 9223372036854775807, 9223372036854775807, 4611686018427387903},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, request, result := fundedSTSRequest(t, "sts")
			ctx := context.Background()
			store := NewSTSVendStore(f.pool, "test-installation")
			if _, err := services.NewSTSVendingService(store, &vendingProvider{fail: true}).Vend(ctx, request); !errors.Is(err, domain.ErrVendUncertain) {
				t.Fatalf("reserve uncertain vend: %v", err)
			}
			if _, err := f.pool.Exec(ctx, "UPDATE energy_credits SET kwh_granted=$2::numeric,money_value_minor_units=$3 WHERE credit_id=$1", result.Credit.CreditID, tc.creditKWh, tc.creditMoney); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, "UPDATE credit_balances SET remaining_kwh=$2::numeric,remaining_money_value_minor_units=$3 WHERE assignment_id=$1", result.Credit.AssignmentID, tc.balanceKWh, tc.balanceMoney); err != nil {
				t.Fatal(err)
			}
			r := domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: "operator:test", Evidence: "provider definitive non-issuance", NotIssued: true}
			if err := services.ReconcileSTSVend(ctx, store, r); err != nil {
				t.Fatal(err)
			}
			var kwh string
			var money int64
			var conserved bool
			if err := f.pool.QueryRow(ctx, `
				SELECT scr.kwh::text, scr.money_minor,
				       scr.kwh+scr.shortfall_kwh=$2::numeric
				       AND scr.money_minor+scr.shortfall_money_minor=$3::numeric
				       AND cb.remaining_kwh+scr.kwh=$4::numeric
				       AND cb.remaining_money_value_minor_units+scr.money_minor=$5::numeric
				FROM sts_credit_reversals scr JOIN credit_balances cb ON cb.assignment_id=$6::uuid
				WHERE scr.reference=$1`, request.Reference, tc.creditKWh, tc.creditMoney, tc.balanceKWh, tc.balanceMoney, result.Credit.AssignmentID).Scan(&kwh, &money, &conserved); err != nil {
				t.Fatal(err)
			}
			if kwh != tc.reversedKWh || money != tc.reversedMoney || !conserved {
				t.Fatalf("reversed=%s kWh/%d minor, want=%s/%d; conservation=%t", kwh, money, tc.reversedKWh, tc.reversedMoney, conserved)
			}
			if err := services.ReconcileSTSVend(ctx, store, r); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("second reversal was not refused: %v", err)
			}
		})
	}
}

func TestMissingBalanceRowRecordsWholeCreditAsShortfall(t *testing.T) {
	f, request, result := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	store := NewSTSVendStore(f.pool, "test-installation")
	services.NewSTSVendingService(store, &vendingProvider{fail: true}).Vend(ctx, request)
	// A settled balance may be deleted; the provider's non-issuance must still be recordable.
	if _, err := f.pool.Exec(ctx, "UPDATE credit_balances SET remaining_kwh=0,remaining_money_value_minor_units=0 WHERE assignment_id=$1", result.Credit.AssignmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "DELETE FROM credit_balances WHERE assignment_id=$1", result.Credit.AssignmentID); err != nil {
		t.Fatal(err)
	}
	if err := services.ReconcileSTSVend(ctx, store, domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: "operator:test", Evidence: "provider definitive non-issuance", NotIssued: true}); err != nil {
		t.Fatalf("non-issuance blocked by a missing balance: %v", err)
	}
	var reversedKWh, shortfallKWh string
	var reversedMoney, shortfallMoney int64
	if err := f.pool.QueryRow(ctx, "SELECT kwh::text,shortfall_kwh::text,money_minor,shortfall_money_minor FROM sts_credit_reversals WHERE reference=$1", request.Reference).Scan(&reversedKWh, &shortfallKWh, &reversedMoney, &shortfallMoney); err != nil {
		t.Fatal(err)
	}
	if reversedKWh != "0.000000" || shortfallKWh != "0.200040" || reversedMoney != 0 || shortfallMoney != 5001 {
		t.Fatalf("reversal = %s kWh (shortfall %s), %d minor (shortfall %d); want all of it as shortfall", reversedKWh, shortfallKWh, reversedMoney, shortfallMoney)
	}
}

func TestLegacyNonIssuanceCanRepairItsMissingBalanceReversal(t *testing.T) {
	f, request, result := fundedSTSRequest(t, "sts")
	ctx := context.Background()
	store := NewSTSVendStore(f.pool, "test-installation")
	services.NewSTSVendingService(store, &vendingProvider{fail: true}).Vend(ctx, request)
	// This is the persisted state produced by the previous release: a funded
	// non-issuance with its balance untouched and no compensating entry.
	var originalAt time.Time
	if err := f.pool.QueryRow(ctx, "UPDATE sts_vends SET not_issued_at=now() WHERE reference=$1 RETURNING not_issued_at", request.Reference).Scan(&originalAt); err != nil {
		t.Fatal(err)
	}
	records, err := store.ListUnresolved(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range records {
		if r.Reference == request.Reference {
			found = r.NeedsBalanceReversal
		}
	}
	if !found {
		t.Fatal("legacy uncorrected balance hidden from operator")
	}
	r := domain.STSReconciliation{Reference: request.Reference, MeterID: request.MeterID, Actor: "operator:test", Evidence: "repair of previously confirmed provider non-issuance", NotIssued: true}
	if err := services.ReconcileSTSVend(ctx, store, r); err != nil {
		t.Fatal(err)
	}
	if err := services.ReconcileSTSVend(ctx, store, r); err == nil {
		t.Fatal("legacy compensation applied twice")
	}
	var kwh float64
	var money int64
	var closedAt time.Time
	if err := f.pool.QueryRow(ctx, "SELECT remaining_kwh,remaining_money_value_minor_units FROM credit_balances WHERE assignment_id=$1", result.Credit.AssignmentID).Scan(&kwh, &money); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, "SELECT not_issued_at FROM sts_vends WHERE reference=$1", request.Reference).Scan(&closedAt); err != nil {
		t.Fatal(err)
	}
	if kwh != 0 || money != 0 || !closedAt.Equal(originalAt) {
		t.Fatalf("repair changed history or left credit: %v %d %s (was %s)", kwh, money, closedAt, originalAt)
	}
}
