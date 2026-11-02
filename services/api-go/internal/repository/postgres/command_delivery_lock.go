package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Polling, evidence capture, and replacement creation share this transaction
// lock. It linearizes dispatch against late evidence, before either locks rows.
func lockSiteCommandDelivery(ctx context.Context, tx pgx.Tx, siteID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('meter-delivery:'||$1,0))`, siteID); err != nil {
		return fmt.Errorf("lock site command delivery: %w", err)
	}
	return nil
}
