package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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

	for _, transition := range []struct {
		status domain.EventStatus
		apply  func(context.Context, uint64) error
	}{
		{domain.StatusPendingUpload, store.MarkPendingUpload},
		{domain.StatusUploaded, store.MarkUploaded},
		{domain.StatusAcknowledged, store.Ack},
		{domain.StatusExpired, store.Expire},
	} {
		if err := transition.apply(ctx, sequence); err != nil {
			t.Fatalf("transition to %s: %v", transition.status, err)
		}
		assertRecordStatus(t, store, sequence, transition.status)
	}

	if err := store.MarkUploaded(ctx, sequence); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("transition expired event error = %v, want ErrInvalidTransition", err)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("read queue stats: %v", err)
	}
	if stats.Depth != 0 {
		t.Fatalf("active queue depth = %d, want 0", stats.Depth)
	}
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
