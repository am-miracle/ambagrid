package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

func TestHeartbeatSequenceIsDurableAndNeverReused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1730000000, 0).UTC()

	first, err := store.ReserveHeartbeat(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHeartbeat(ctx, first, "modem unavailable"); err != nil {
		t.Fatal(err)
	}
	state, err := store.HeartbeatState(ctx, start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if state.LastAttemptAt == nil || !state.LastAttemptAt.Equal(start) || !state.LastAttemptFailed || state.AttemptsLastDay != 1 {
		t.Fatalf("state after failed heartbeat = %+v", state)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Far enough ahead that the first attempt is pruned.
	later := start.Add(8 * 24 * time.Hour)
	second, err := store.ReserveHeartbeat(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Fatalf("heartbeat sequence after restart and prune = %d, want > %d", second, first)
	}
	if err := store.RecordHeartbeat(ctx, second, ""); err != nil {
		t.Fatal(err)
	}
	state, err = store.HeartbeatState(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastAttemptFailed || state.AttemptsLastDay != 1 {
		t.Fatalf("state after successful heartbeat = %+v", state)
	}
	if err := store.RecordHeartbeat(ctx, second+1, ""); err == nil {
		t.Fatal("recorded a heartbeat that was never reserved")
	}
}

func TestOpenMigratesVersionEightQueueWithHeartbeats(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE command_reports; DROP TABLE command_ledger; DROP TABLE heartbeat_attempts; PRAGMA user_version = 8;`); err != nil {
		t.Fatalf("downgrade to version eight: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatalf("migrate version eight queue: %v", err)
	}
	defer store.Close()
	sequence, err := store.ReserveHeartbeat(ctx, time.Unix(1730000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 1 {
		t.Fatalf("first heartbeat sequence after migration = %d, want 1", sequence)
	}
}
