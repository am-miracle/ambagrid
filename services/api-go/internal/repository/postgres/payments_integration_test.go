package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPaymentServiceAppliesConcurrentDuplicateOnceInPostgres(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires DATABASE_URL and engine migrations")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	siteID := "it-site-" + suffix
	assetID := "it-asset-" + suffix
	meterID := "it-meter-" + suffix
	customerID := "it-customer-" + suffix
	tariffID := "it-tariff-" + suffix
	reference := "it-payment-" + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed database: %v", err)
		}
	}
	mustExec("INSERT INTO sites (site_id, name) VALUES ($1, 'Payment integration test')", siteID)
	mustExec("INSERT INTO assets (asset_id, site_id, asset_type, last_seen_at) VALUES ($1, $2, 'smart_meter', now())", assetID, siteID)
	mustExec("INSERT INTO customers (customer_id, site_id, display_name) VALUES ($1, $2, 'Payment integration customer')", customerID, siteID)
	mustExec("INSERT INTO smart_meters (meter_id, site_id, asset_id) VALUES ($1, $2, $3)", meterID, siteID, assetID)
	mustExec("INSERT INTO meter_assignments (site_id, meter_id, customer_id) VALUES ($1, $2, $3)", siteID, meterID, customerID)
	mustExec("INSERT INTO tariff_plans (tariff_plan_id, site_id, name, currency, price_per_kwh, effective_from) VALUES ($1, $2, 'Integration tariff', 'NGN', 250, now() - interval '1 day')", tariffID, siteID)

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM command_event_outbox WHERE aggregate_id IN (SELECT payment_id::text FROM payments WHERE external_reference = $1) OR aggregate_id IN (SELECT credit_id::text FROM energy_credits WHERE payment_id IN (SELECT payment_id FROM payments WHERE external_reference = $1))", reference)
		_, _ = pool.Exec(ctx, "DELETE FROM audit_events WHERE subject_type = 'payment' AND subject_id IN (SELECT payment_id::text FROM payments WHERE external_reference = $1)", reference)
		_, _ = pool.Exec(ctx, "DELETE FROM credit_balances WHERE assignment_id IN (SELECT assignment_id FROM meter_assignments WHERE customer_id = $1)", customerID)
		_, _ = pool.Exec(ctx, "DELETE FROM energy_credits WHERE payment_id IN (SELECT payment_id FROM payments WHERE external_reference = $1)", reference)
		_, _ = pool.Exec(ctx, "DELETE FROM payments WHERE external_reference = $1", reference)
		_, _ = pool.Exec(ctx, "DELETE FROM meter_assignments WHERE customer_id = $1", customerID)
		_, _ = pool.Exec(ctx, "DELETE FROM tariff_plans WHERE tariff_plan_id = $1", tariffID)
		_, _ = pool.Exec(ctx, "DELETE FROM smart_meters WHERE meter_id = $1", meterID)
		_, _ = pool.Exec(ctx, "DELETE FROM customers WHERE customer_id = $1", customerID)
		_, _ = pool.Exec(ctx, "DELETE FROM assets WHERE asset_id = $1", assetID)
		_, _ = pool.Exec(ctx, "DELETE FROM sites WHERE site_id = $1", siteID)
	})

	store := NewStore(pool, 5*time.Second)
	service := services.NewPaymentService(store)
	command := domain.ApplyPaymentCommand{
		Provider:          "integration",
		ExternalReference: reference,
		CustomerID:        customerID,
		AmountMinorUnits:  500000,
		Currency:          "NGN",
		Status:            domain.PaymentStatusConfirmed,
		ConfirmedAt:       time.Now().UTC(),
	}

	results := make([]domain.ApplyPaymentResult, 2)
	errors := make([]error, 2)
	var wait sync.WaitGroup
	for i := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errors[index] = service.ApplyWebhookPayment(ctx, command)
		}(i)
	}
	wait.Wait()

	for i, err := range errors {
		if err != nil {
			t.Fatalf("concurrent call %d: %v", i, err)
		}
	}
	if results[0].Payment.PaymentID != results[1].Payment.PaymentID {
		t.Fatalf("payment IDs differ: %q != %q", results[0].Payment.PaymentID, results[1].Payment.PaymentID)
	}

	for table, query := range map[string]string{
		"payments":       "SELECT count(*) FROM payments WHERE external_reference = $1",
		"energy_credits": "SELECT count(*) FROM energy_credits WHERE payment_id = $1::uuid",
	} {
		var count int
		argument := any(reference)
		if table == "energy_credits" {
			argument = results[0].Payment.PaymentID
		}
		if err := pool.QueryRow(ctx, query, argument).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s count = %d, want 1", table, count)
		}
	}

	var eventCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM command_event_outbox WHERE aggregate_id IN ($1, $2)", results[0].Payment.PaymentID, results[0].Credit.CreditID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 2 {
		t.Fatalf("revenue event count = %d, want 2", eventCount)
	}
	if results[0].Credit.KWhGranted != 20 || results[1].Credit.KWhGranted != 20 {
		t.Fatalf("credit mismatch: %.2f / %.2f", results[0].Credit.KWhGranted, results[1].Credit.KWhGranted)
	}
}
