package fallback_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"edge-agent-go/internal/fallback"
)

type commandCall struct {
	name string
	args []string
}

type fakeRunner struct {
	calls []commandCall
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, commandCall{name: name, args: args})
	if len(r.calls) == 1 {
		return []byte("Successfully created new SMS: /org/freedesktop/ModemManager1/SMS/12"), nil
	}
	return []byte("successfully sent the SMS"), nil
}

type deadlineCapturingRunner struct {
	deadlines   []time.Time
	hasDeadline []bool
}

func (r *deadlineCapturingRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	dl, ok := ctx.Deadline()
	r.deadlines = append(r.deadlines, dl)
	r.hasDeadline = append(r.hasDeadline, ok)
	if len(r.hasDeadline) == 1 {
		return []byte("Successfully created new SMS: /org/freedesktop/ModemManager1/SMS/12"), nil
	}
	return []byte("successfully sent the SMS"), nil
}

type blockingRunner struct {
	done chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.done:
		return []byte("Successfully created new SMS: /org/freedesktop/ModemManager1/SMS/12"), nil
	}
}

func TestModemManagerSenderCreatesAndSendsSMS(t *testing.T) {
	runner := &fakeRunner{}
	sender := fallback.NewModemManagerSender("0", runner, 0)

	if err := sender.Send(context.Background(), "+2348000000000", "AMBAGRID|v=1"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	want := []commandCall{
		{name: "mmcli", args: []string{"-m", "0", "--messaging-create-sms=text=AMBAGRID|v=1,number=+2348000000000"}},
		{name: "mmcli", args: []string{"-s", "/org/freedesktop/ModemManager1/SMS/12", "--send"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestSendAppliesDeadlineToBothCommands(t *testing.T) {
	runner := &deadlineCapturingRunner{}
	sender := fallback.NewModemManagerSender("0", runner, 10*time.Second)

	before := time.Now()
	if err := sender.Send(context.Background(), "+2348000000000", "test"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	for i, has := range runner.hasDeadline {
		if !has {
			t.Fatalf("command %d context has no deadline", i)
		}
		margin := runner.deadlines[i].Sub(before)
		if margin < 0 || margin > 11*time.Second {
			t.Fatalf("command %d deadline out of range: %v from call start", i, margin)
		}
	}
}

func TestSendRespectsCallerDeadline(t *testing.T) {
	runner := &deadlineCapturingRunner{}
	sender := fallback.NewModemManagerSender("0", runner, 30*time.Second)

	callerDeadline := time.Now().Add(2 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
	defer cancel()

	if err := sender.Send(ctx, "+2348000000000", "test"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	for i, dl := range runner.deadlines {
		if dl.After(callerDeadline) {
			t.Fatalf("command %d deadline %v exceeds caller deadline %v", i, dl, callerDeadline)
		}
	}
}

func TestSendReturnsAfterTimeout(t *testing.T) {
	runner := &blockingRunner{done: make(chan struct{})}
	sender := fallback.NewModemManagerSender("0", runner, 100*time.Millisecond)

	start := time.Now()
	err := sender.Send(context.Background(), "+2348000000000", "test")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from timed-out Send")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Send blocked for %v, expected ~100ms", elapsed)
	}
}
