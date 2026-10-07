package ingest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresSiteHealthStore struct {
	pool *pgxpool.Pool
}

func NewPostgresSiteHealthStore(pool *pgxpool.Pool) *PostgresSiteHealthStore {
	return &PostgresSiteHealthStore{pool: pool}
}

func (s *PostgresSiteHealthStore) RecordContact(ctx context.Context, report SiteHealthReport) error {
	// Older concurrent reports must not roll the site's health backward.
	_, err := s.pool.Exec(ctx, `
INSERT INTO site_health(
    site_id, gateway_id, last_contact_at, last_event_timestamp,
    queue_depth, oldest_pending_at, queue_growing
) VALUES ($1, $2, $3, $4, $5, $6, false)
ON CONFLICT (site_id) DO UPDATE SET
    gateway_id = EXCLUDED.gateway_id,
    last_contact_at = EXCLUDED.last_contact_at,
    last_event_timestamp = CASE
        WHEN site_health.last_event_timestamp IS NULL THEN EXCLUDED.last_event_timestamp
        WHEN EXCLUDED.last_event_timestamp IS NULL THEN site_health.last_event_timestamp
        ELSE greatest(site_health.last_event_timestamp, EXCLUDED.last_event_timestamp)
    END,
    queue_growing = CASE
        WHEN EXCLUDED.queue_depth > site_health.queue_depth THEN true
        ELSE false
    END,
    queue_depth = EXCLUDED.queue_depth,
    oldest_pending_at = EXCLUDED.oldest_pending_at
WHERE EXCLUDED.last_contact_at >= site_health.last_contact_at`,
		report.SiteID, report.GatewayID, report.ContactAt, report.LastEventTimestamp,
		report.QueueDepth, report.OldestPendingAt,
	)
	if err != nil {
		return fmt.Errorf("upsert site health: %w", err)
	}
	return nil
}
