package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationPool closes the pool through t.Cleanup. Registered before any data
// cleanup, it runs after all of them; a deferred Close would run first and turn
// every cleanup statement into an error.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires DATABASE_URL and engine migrations")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// cleaner fails the test when a cleanup statement fails, so leaked rows that
// break later tests on a shared database are noticed where they start.
func cleaner(t *testing.T, pool *pgxpool.Pool) func(sql string, args ...any) {
	return func(sql string, args ...any) {
		if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
			t.Errorf("clean up integration data: %v", err)
		}
	}
}
