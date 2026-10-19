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
	record, err := store.Record(ctx, sequence)
	if err != nil {
		t.Fatal(err)
	}
	if !record.ReplayRequired {
		t.Fatal("failed upload did not persist replay_required")
	}
}

func TestEventsCollectedDuringOutageRequireReplayUntilContactRecovers(t *testing.T) {
	ctx := context.Background()
	store := openTestQueue(t)
	defer store.Close()

	first, err := store.Enqueue(ctx, testEvent([]byte("first"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(ctx, first, time.Now().Add(time.Minute), "network unavailable"); err != nil {
		t.Fatal(err)
	}

	buffered, err := store.Enqueue(ctx, testEvent([]byte("buffered"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	bufferedRecord, err := store.Record(ctx, buffered)
	if err != nil {
		t.Fatal(err)
	}
	if !bufferedRecord.ReplayRequired {
		t.Fatal("record collected during outage did not require replay")
	}

	if err := store.RecordUploadContactSuccess(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	catchingUp, err := store.Enqueue(ctx, testEvent([]byte("catching-up"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	catchingUpRecord, err := store.Record(ctx, catchingUp)
	if err != nil {
		t.Fatal(err)
	}
	if !catchingUpRecord.ReplayRequired {
		t.Fatal("record collected while replay backlog remained did not require replay")
	}

	for _, sequence := range []uint64{first, buffered, catchingUp} {
		if err := store.MarkUploaded(ctx, sequence, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := store.Ack(ctx, sequence); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordUploadContactSuccess(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	prompt, err := store.Enqueue(ctx, testEvent([]byte("prompt"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	promptRecord, err := store.Record(ctx, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if promptRecord.ReplayRequired {
		t.Fatal("record collected after contact recovered still required replay")
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
	record, err := store.Record(ctx, sequence)
	if err != nil {
		t.Fatal(err)
	}
	if !record.ReplayRequired {
		t.Fatal("restart-recovered persisted event did not require replay")
	}
	buffered, err := store.Enqueue(ctx, testEvent([]byte("collected-during-recovery"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	bufferedRecord, err := store.Record(ctx, buffered)
	if err != nil {
		t.Fatal(err)
	}
	if !bufferedRecord.ReplayRequired {
		t.Fatal("record collected while restart backlog remained did not require replay")
	}
}

func TestOpenRecoversUploadedEventsForIdempotentRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := store.Enqueue(ctx, testEvent([]byte("reading"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkUploaded(ctx, sequence, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertRecordStatus(t, store, sequence, domain.StatusPendingUpload)
	record, err := store.Record(ctx, sequence)
	if err != nil {
		t.Fatal(err)
	}
	if !record.ReplayRequired {
		t.Fatal("restart-recovered uploaded event did not require replay")
	}
}

func TestHeartbeatFailureActivatesReplayMode(t *testing.T) {
	ctx := context.Background()
	store := openTestQueue(t)
	defer store.Close()

	if err := store.RecordUploadContactFailure(ctx, "connection refused"); err != nil {
		t.Fatal(err)
	}

	seq, err := store.Enqueue(ctx, testEvent([]byte("during-outage"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Record(ctx, seq)
	if err != nil {
		t.Fatal(err)
	}
	if !record.ReplayRequired {
		t.Fatal("record collected after heartbeat failure should require replay")
	}
}

func TestPurgeDeletesAcknowledgedRecords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestQueue(t)
	defer store.Close()

	seq, err := store.Enqueue(ctx, testEvent([]byte("old-reading"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkUploaded(ctx, seq, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Ack(ctx, seq); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.Purge(ctx, 0)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("purge deleted %d, want 1", deleted)
	}

	_, err = store.Record(ctx, seq)
	if !errors.Is(err, domain.ErrEventNotFound) {
		t.Fatalf("expected ErrEventNotFound after purge, got %v", err)
	}
}

func TestPurgeRespectsRetentionAge(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestQueue(t)
	defer store.Close()

	seq, err := store.Enqueue(ctx, testEvent([]byte("recent"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkUploaded(ctx, seq, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Ack(ctx, seq); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.Purge(ctx, 1*time.Hour)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("purge should not delete recent records, deleted %d", deleted)
	}

	_, err = store.Record(ctx, seq)
	if err != nil {
		t.Fatalf("record should still exist: %v", err)
	}
}

func TestPurgeDoesNotDeleteActiveRecords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestQueue(t)
	defer store.Close()

	seq, err := store.Enqueue(ctx, testEvent([]byte("active"), domain.PriorityNormal))
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := store.Purge(ctx, 0)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("purge should not delete active records, deleted %d", deleted)
	}

	assertRecordStatus(t, store, seq, domain.StatusPendingUpload)
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
