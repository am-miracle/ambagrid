package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"edge-agent-go/internal/domain"
	queuesqlite "edge-agent-go/internal/repository/sqlite"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesVersionOneQueue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open version one database: %v", err)
	}
	_, err = db.ExecContext(ctx, `
CREATE TABLE edge_identity (
    singleton INTEGER PRIMARY KEY, site_id TEXT NOT NULL, gateway_id TEXT NOT NULL
);
INSERT INTO edge_identity VALUES (1, 'ng-kaji-01', 'gateway-01');
CREATE TABLE queue_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    site_id TEXT NOT NULL, gateway_id TEXT NOT NULL, device_id TEXT NOT NULL,
    mqtt_topic TEXT NOT NULL, payload BLOB NOT NULL, payload_bytes INTEGER NOT NULL,
    priority INTEGER NOT NULL, event_at_ms INTEGER NOT NULL, received_at_ms INTEGER NOT NULL,
    persisted_at_ms INTEGER NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at_ms INTEGER, last_error TEXT
);
INSERT INTO queue_events (
    site_id, gateway_id, device_id, mqtt_topic, payload, payload_bytes,
    priority, event_at_ms, received_at_ms, persisted_at_ms
) VALUES ('ng-kaji-01', 'gateway-01', 'meter-01',
          'africa-west/ng-kaji-01/smartmeter/meter-01/telemetry', '{}', 2, 0, 1, 2, 3);
PRAGMA user_version = 1;
`)
	if err != nil {
		t.Fatalf("create version one database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version one database: %v", err)
	}

	store, err := queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatalf("migrate version one queue: %v", err)
	}
	defer store.Close()
	record, err := store.Record(ctx, 1)
	if err != nil {
		t.Fatalf("read migrated record: %v", err)
	}
	if record.AssetType != domain.AssetSmartMeter || record.Status != domain.StatusPendingUpload {
		t.Fatalf("migrated record = asset type %q status %q", record.AssetType, record.Status)
	}
}
