package sqlite

import (
	"context"
	"fmt"

	"edge-agent-go/internal/domain"
)

const schemaVersion = 3

const schema = `
CREATE TABLE IF NOT EXISTS edge_identity (
    singleton  INTEGER PRIMARY KEY CHECK (singleton = 1),
    site_id    TEXT NOT NULL,
    gateway_id TEXT NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS queue_events (
    sequence            INTEGER PRIMARY KEY AUTOINCREMENT,
    site_id             TEXT NOT NULL,
    gateway_id          TEXT NOT NULL,
    device_id           TEXT NOT NULL,
    asset_type          TEXT NOT NULL CHECK (asset_type IN ('smart_meter', 'battery_bms', 'solar_inverter')),
    mqtt_topic          TEXT NOT NULL,
    payload             BLOB NOT NULL,
    payload_bytes       INTEGER NOT NULL CHECK (payload_bytes >= 0),
    priority            INTEGER NOT NULL CHECK (priority IN (0, 1)),
    event_at_ms         INTEGER NOT NULL,
    received_at_ms      INTEGER NOT NULL,
    persisted_at_ms     INTEGER NOT NULL,
    uploaded_at_ms      INTEGER,
    status              TEXT NOT NULL CHECK (status IN ('persisted', 'pending_upload', 'uploaded', 'acknowledged', 'expired')),
    attempt_count       INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at_ms  INTEGER,
    last_error          TEXT
) STRICT;

CREATE INDEX IF NOT EXISTS queue_events_ready
    ON queue_events(status, next_attempt_at_ms, sequence);
`

const migrateV1ToV2 = `
ALTER TABLE queue_events ADD COLUMN asset_type TEXT NOT NULL DEFAULT 'smart_meter'
    CHECK (asset_type IN ('smart_meter', 'battery_bms', 'solar_inverter'));
ALTER TABLE queue_events ADD COLUMN status TEXT NOT NULL DEFAULT 'pending_upload'
    CHECK (status IN ('persisted', 'pending_upload', 'uploaded', 'acknowledged', 'expired'));
DROP INDEX IF EXISTS queue_events_ready;
CREATE INDEX queue_events_ready ON queue_events(status, next_attempt_at_ms, sequence);
`

const migrateV2ToV3 = `
ALTER TABLE queue_events ADD COLUMN uploaded_at_ms INTEGER;
`

func (s *Store) initialize(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read queue schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("queue schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version < schemaVersion {
		if err := s.migrateSchema(ctx, version); err != nil {
			return err
		}
	}
	if err := s.configureStorageLimits(ctx); err != nil {
		return err
	}
	if err := s.ensureIdentity(ctx); err != nil {
		return err
	}
	return s.recoverPersisted(ctx)
}

func (s *Store) migrateSchema(ctx context.Context, version int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin queue schema migration: %w", err)
	}
	defer tx.Rollback()

	// Fresh databases get the full schema; existing ones apply only the
	// incremental migrations they haven't seen yet. The base schema already
	// includes all columns, so running incremental ALTERs on version 0
	// would fail with duplicate column errors.
	if version == 0 {
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("create queue schema: %w", err)
		}
	} else {
		incremental := []struct {
			from      int
			statement string
			desc      string
		}{
			{1, migrateV1ToV2, "migrate queue schema from version 1 to 2"},
			{2, migrateV2ToV3, "migrate queue schema from version 2 to 3"},
		}
		for _, m := range incremental {
			if version > m.from {
				continue
			}
			if _, err := tx.ExecContext(ctx, m.statement); err != nil {
				return fmt.Errorf("%s: %w", m.desc, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("record queue schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit queue schema migration: %w", err)
	}
	return nil
}

func (s *Store) configureStorageLimits(ctx context.Context) error {
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&s.pageSize); err != nil {
		return fmt.Errorf("read sqlite page size: %w", err)
	}
	maxPages := s.databaseBudget() / s.pageSize
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count = %d", maxPages)); err != nil {
		return fmt.Errorf("limit sqlite queue size: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA journal_size_limit = %d", walBudget(s.cfg.MaxStorageBytes))); err != nil {
		return fmt.Errorf("limit sqlite WAL size: %w", err)
	}
	autocheckpointPages := max(int64(1), walBudget(s.cfg.MaxStorageBytes)/s.pageSize)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA wal_autocheckpoint = %d", autocheckpointPages)); err != nil {
		return fmt.Errorf("configure sqlite WAL checkpoints: %w", err)
	}
	return nil
}

func (s *Store) ensureIdentity(ctx context.Context) error {
	result, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO edge_identity(singleton, site_id, gateway_id) VALUES (1, ?, ?)",
		s.cfg.SiteID, s.cfg.GatewayID,
	)
	if err != nil {
		return fmt.Errorf("initialize edge identity: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read identity initialization result: %w", err)
	}
	if inserted > 0 {
		return nil
	}
	var siteID, gatewayID string
	if err := s.db.QueryRowContext(ctx, "SELECT site_id, gateway_id FROM edge_identity WHERE singleton = 1").Scan(&siteID, &gatewayID); err != nil {
		return fmt.Errorf("read edge identity: %w", err)
	}
	if siteID != s.cfg.SiteID || gatewayID != s.cfg.GatewayID {
		return fmt.Errorf("%w: database has site %q gateway %q, configured site %q gateway %q", domain.ErrIdentityMismatch, siteID, gatewayID, s.cfg.SiteID, s.cfg.GatewayID)
	}
	return nil
}
