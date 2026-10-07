package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"edge-agent-go/internal/domain"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

func TestQueueEventLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestQueue(t)
	defer store.Close()

	sequence, err := store.Persist(ctx, testEvent([]byte("reading"), domain.PriorityNormal))
	if err != nil {
		t.Fatalf("persist event: %v", err)
	}
	assertRecordStatus(t, store, sequence, domain.StatusPersisted)

	if err := store.MarkPendingUpload(ctx, sequence); err != nil {
		t.Fatalf("transition to pending_upload: %v", err)
	}
	assertRecordStatus(t, store, sequence, domain.StatusPendingUpload)

	if err := store.MarkUploaded(ctx, sequence, time.Now().UTC()); err != nil {
		t.Fatalf("transition to uploaded: %v", err)
	}
	assertRecordStatus(t, store, sequence, domain.StatusUploaded)

	if err := store.Ack(ctx, sequence); err != nil {
		t.Fatalf("transition to acknowledged: %v", err)
	}
	assertRecordStatus(t, store, sequence, domain.StatusAcknowledged)

	if err := store.Expire(ctx, sequence); err != nil {
		t.Fatalf("transition to expired: %v", err)
	}
	assertRecordStatus(t, store, sequence, domain.StatusExpired)

	if err := store.MarkUploaded(ctx, sequence, time.Now().UTC()); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("transition expired event error = %v, want ErrInvalidTransition", err)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("read queue stats: %v", err)
	}
	if stats.Depth != 0 {
		t.Fatalf("active queue depth = %d, want 0", stats.Depth)
	}
	if stats.LastEventTimestamp == nil {
		t.Fatal("last event timestamp was lost after the queue drained")
	}
	if stats.OldestPendingAt != nil {
		t.Fatalf("oldest pending timestamp = %v, want nil for a drained queue", stats.OldestPendingAt)
	}
}

func TestUploadFailureReturnsUploadedEventToPending(t *testing.T) {
	ctx := context.Background()
	store := openTestQueue(t)
	defer store.Close()
	sequence, err := store.Enqueue(ctx, testEvent([]byte("reading"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkUploaded(ctx, sequence, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(ctx, sequence, time.Now().Add(time.Minute), "network unavailable"); err != nil {
		t.Fatal(err)
	}
	assertRecordStatus(t, store, sequence, domain.StatusPendingUpload)
}

func TestOpenRecoversPersistedEventsForUpload(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	sequence, err := store.Persist(ctx, testEvent([]byte("reading"), domain.PriorityNormal))
	if err != nil {
		t.Fatalf("persist event: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close queue: %v", err)
	}

	store, err = queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatalf("reopen queue: %v", err)
	}
	defer store.Close()
	assertRecordStatus(t, store, sequence, domain.StatusPendingUpload)
}

func assertRecordStatus(t *testing.T, store *queuesqlite.Store, sequence uint64, want domain.EventStatus) {
	t.Helper()
	record, err := store.Record(context.Background(), sequence)
	if err != nil {
		t.Fatalf("read record %d: %v", sequence, err)
	}
	if record.Status != want {
		t.Fatalf("record %d status = %q, want %q", sequence, record.Status, want)
	}
}
