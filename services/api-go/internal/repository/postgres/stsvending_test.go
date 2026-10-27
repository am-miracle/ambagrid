package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"api-go/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSTSStoreErrorsIdentifyOperationAndPreserveCause(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	// A closed pool fails deterministically without contacting a database.
	pool.Close()
	_, cause := pool.Acquire(ctx)
	store := NewSTSVendStore(pool, "test-installation")
	for _, tc := range []struct {
		name, operation string
		run             func() error
	}{
		{"open", "acquire STS vend connection", func() error { _, err := store.Open(ctx, "credit-1"); return err }},
		{"reconcile", "begin STS reconciliation", func() error {
			return store.Reconcile(ctx, domain.STSReconciliation{Reference: "credit-1", MeterID: "12345678901", Actor: "operator:test", Evidence: "provider verified", NotIssued: true})
		}},
		{"unresolved", "list unresolved STS vends", func() error { _, err := store.ListUnresolved(ctx); return err }},
		{"shortfalls", "list STS reversal shortfalls", func() error { _, err := store.ListReversalShortfalls(ctx); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), tc.operation+": ") {
				t.Fatalf("missing operation %q: %v", tc.operation, err)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("lost cause %v: %v", cause, err)
			}
		})
	}
}
