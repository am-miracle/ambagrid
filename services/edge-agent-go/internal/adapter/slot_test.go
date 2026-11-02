package adapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
)

func TestSlotRefusesEndedContextEvenWhenFree(t *testing.T) {
	slot := adapter.NewSlot()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()

	// Without the post-acquire check select takes the free slot about half the
	// time, so 1000 clean attempts leave a 2^-1000 chance of missing the bug.
	for range 1000 {
		if release, err := slot.Acquire(cancelled); !errors.Is(err, context.Canceled) {
			if release != nil {
				release()
			}
			t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
		}
		if release, err := slot.Acquire(expired); !errors.Is(err, adapter.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			if release != nil {
				release()
			}
			t.Fatalf("expired acquire error = %v, want ErrTimeout wrapping context.DeadlineExceeded", err)
		}
	}
	release, err := slot.Acquire(context.Background())
	if err != nil {
		t.Fatalf("refused acquires leaked the slot: %v", err)
	}
	release()
}

func TestSlotQueuedAcquireGivesUpPromptly(t *testing.T) {
	slot := adapter.NewSlot()
	release, err := slot.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = slot.Acquire(ctx)
	if !errors.Is(err, adapter.ErrTimeout) {
		t.Fatalf("queued acquire error = %v, want ErrTimeout", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("queued acquire returned %s after its deadline", waited)
	}
}

func TestSlotReleaseIsIdempotent(t *testing.T) {
	slot := adapter.NewSlot()
	first, err := slot.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first()
	second, err := slot.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	first()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	// No release on failure: with an unguarded release it would block forever.
	if _, err := slot.Acquire(ctx); err == nil {
		t.Fatal("a repeated release freed the slot held by another caller")
	}
}
