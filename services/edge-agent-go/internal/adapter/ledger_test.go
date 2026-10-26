package adapter_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

var meterCaps = adapter.Capabilities{
	DeviceType: "smart_meter", Manufacturer: "test", Model: "meter",
	Capabilities: adapter.CapabilitySet{Telemetry: true, RelayControl: true, PrepaidCredit: true, STSToken: true},
}

var errNoReply = errors.New("serial read: no reply")

// fakeMeter applies every command that reaches it; loseReplies drops the
// reply after applying, which is the failure the ledger exists to survive.
type fakeMeter struct {
	caps adapter.Capabilities

	mu          sync.Mutex
	applied     []adapter.Command
	loseReplies int
	reject      bool
	failBefore  error
	afterApply  func()
	skipHook    bool
}

func newMeter() *fakeMeter { return &fakeMeter{caps: meterCaps} }

func (m *fakeMeter) Capabilities() adapter.Capabilities { return m.caps }

func (m *fakeMeter) Execute(ctx context.Context, cmd adapter.Command) (adapter.CommandResult, error) {
	m.mu.Lock()
	failBefore, afterApply := m.failBefore, m.afterApply
	m.mu.Unlock()
	if failBefore != nil {
		return adapter.CommandResult{}, failBefore
	}
	m.mu.Lock()
	skipHook := m.skipHook
	m.mu.Unlock()
	if !skipHook {
		if err := adapter.BeforeSend(ctx); err != nil {
			return adapter.CommandResult{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reject {
		return adapter.CommandResult{}, fmt.Errorf("%w: E_METER_LOCKED: cover open", adapter.ErrDeviceRejected)
	}
	m.applied = append(m.applied, cmd)
	if afterApply != nil {
		afterApply()
	}
	if m.loseReplies > 0 {
		m.loseReplies--
		return adapter.CommandResult{}, errNoReply
	}
	return adapter.CommandResult{CommandID: cmd.ID, AcknowledgedAt: time.UnixMilli(1790865000000).UTC()}, nil
}

func (m *fakeMeter) appliedCount(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, cmd := range m.applied {
		if cmd.ID == id {
			n++
		}
	}
	return n
}

// reconcilingMeter can report which references it applied.
type reconcilingMeter struct {
	*fakeMeter
	outcomeErr error
}

func (m reconcilingMeter) Outcome(ctx context.Context, cmd adapter.Command) (adapter.CommandResult, bool, error) {
	if m.outcomeErr != nil {
		return adapter.CommandResult{}, false, m.outcomeErr
	}
	if m.appliedCount(cmd.ID) == 0 {
		return adapter.CommandResult{}, false, nil
	}
	return adapter.CommandResult{CommandID: cmd.ID, AcknowledgedAt: time.UnixMilli(1790865060000).UTC()}, true, nil
}

func openStore(t *testing.T, path string) *queuesqlite.Store {
	t.Helper()
	store, err := queuesqlite.Open(context.Background(), queuesqlite.Config{
		Path: path, SiteID: "site-01", GatewayID: "gateway-01",
		MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func tempStore(t *testing.T) *queuesqlite.Store {
	return openStore(t, filepath.Join(t.TempDir(), "queue.db"))
}

func state(t *testing.T, store adapter.LedgerStore, id string) adapter.CommandState {
	t.Helper()
	entry, found, err := store.LoadCommand(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return ""
	}
	return entry.State
}

var (
	kwhCredit  = adapter.Command{ID: "credit-1", Type: adapter.CommandCredit, DeviceID: "meter-01", CreditKWh: 12.5}
	disconnect = adapter.Command{ID: "off-1", Type: adapter.CommandDisconnect, DeviceID: "meter-01"}
)

func TestLedgerNeverResendsCreditAfterLostReply(t *testing.T) {
	store, meter := tempStore(t), newMeter()
	meter.loseReplies = 1
	ledger := adapter.NewLedger(store, meter)
	ctx := context.Background()

	_, err := ledger.Execute(ctx, kwhCredit)
	if !errors.Is(err, adapter.ErrOutcomeUnknown) || !errors.Is(err, errNoReply) {
		t.Fatalf("lost reply error = %v, want ErrOutcomeUnknown wrapping the transport error", err)
	}
	if _, err := ledger.Execute(ctx, kwhCredit); !errors.Is(err, adapter.ErrOutcomeUnknown) {
		t.Fatalf("retry error = %v, want ErrOutcomeUnknown", err)
	}
	if n := meter.appliedCount(kwhCredit.ID); n != 1 {
		t.Fatalf("credit applied %d times, want once", n)
	}
	if got := state(t, store, kwhCredit.ID); got != adapter.CommandUnknown {
		t.Fatalf("state = %q, want unknown", got)
	}
}

func TestLedgerResendsSafeCommandsAfterLostReply(t *testing.T) {
	refCaps := meterCaps
	refCaps.Capabilities.IdempotentRefs = true
	for name, c := range map[string]struct {
		cmd  adapter.Command
		caps adapter.Capabilities
	}{
		"Disconnect":        {disconnect, meterCaps},
		"Status":            {adapter.Command{ID: "status-1", Type: adapter.CommandRequestStatus, DeviceID: "meter-01"}, meterCaps},
		"STSToken":          {adapter.Command{ID: "sts-1", Type: adapter.CommandCredit, DeviceID: "meter-01", STSToken: "12345678901234567890"}, meterCaps},
		"IdempotentRefsKWh": {kwhCredit, refCaps},
	} {
		t.Run(name, func(t *testing.T) {
			store, meter := tempStore(t), newMeter()
			meter.caps, meter.loseReplies = c.caps, 1
			ledger := adapter.NewLedger(store, meter)

			if _, err := ledger.Execute(context.Background(), c.cmd); !errors.Is(err, adapter.ErrOutcomeUnknown) {
				t.Fatalf("lost reply error = %v, want ErrOutcomeUnknown", err)
			}
			result, err := ledger.Execute(context.Background(), c.cmd)
			if err != nil || result.CommandID != c.cmd.ID {
				t.Fatalf("resend = %+v, %v", result, err)
			}
			if n := meter.appliedCount(c.cmd.ID); n != 2 {
				t.Fatalf("command reached the device %d times, want 2", n)
			}
			entry, _, err := store.LoadCommand(context.Background(), c.cmd.ID)
			if err != nil || entry.State != adapter.CommandApplied || entry.Attempts != 2 {
				t.Fatalf("entry = %+v, %v; want applied after 2 attempts", entry, err)
			}
		})
	}
}

// outcomeLossStore simulates power failing after the device acted but before
// the ledger wrote down what happened.
type outcomeLossStore struct{ *queuesqlite.Store }

func (outcomeLossStore) CompleteCommand(context.Context, string, adapter.CommandState, *adapter.CommandResult, string, time.Time) error {
	return errors.New("power lost")
}

func (outcomeLossStore) MarkCommandUnknown(context.Context, string, string, time.Time) error {
	return errors.New("power lost")
}

func TestLedgerSurvivesCrashAfterDeviceApplied(t *testing.T) {
	for name, c := range map[string]struct {
		reconcile   bool
		cmd         adapter.Command
		wantErr     error
		wantApplied int
		wantFinal   adapter.CommandState
	}{
		"CreditStaysUnknown":     {cmd: kwhCredit, wantErr: adapter.ErrOutcomeUnknown, wantApplied: 1, wantFinal: adapter.CommandUnknown},
		"RelayIsResent":          {cmd: disconnect, wantApplied: 2, wantFinal: adapter.CommandApplied},
		"ReconcilerConfirmsOnce": {reconcile: true, cmd: kwhCredit, wantApplied: 1, wantFinal: adapter.CommandApplied},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "queue.db")
			meter := newMeter()
			var inner adapter.CommandAdapter = meter
			if c.reconcile {
				inner = reconcilingMeter{fakeMeter: meter}
			}

			before := openStore(t, path)
			if _, err := adapter.NewLedger(outcomeLossStore{before}, inner).Execute(context.Background(), c.cmd); err != nil {
				t.Fatalf("first attempt: %v", err)
			}
			if got := state(t, before, c.cmd.ID); got != adapter.CommandInFlight {
				t.Fatalf("state before restart = %q, want in_flight", got)
			}
			if err := before.Close(); err != nil {
				t.Fatal(err)
			}

			after := openStore(t, path)
			if got := state(t, after, c.cmd.ID); got != adapter.CommandUnknown {
				t.Fatalf("state after restart = %q, want unknown", got)
			}
			_, err := adapter.NewLedger(after, inner).Execute(context.Background(), c.cmd)
			if c.wantErr == nil && err != nil || c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("retry after restart error = %v, want %v", err, c.wantErr)
			}
			if n := meter.appliedCount(c.cmd.ID); n != c.wantApplied {
				t.Fatalf("command reached the device %d times, want %d", n, c.wantApplied)
			}
			if got := state(t, after, c.cmd.ID); got != c.wantFinal {
				t.Fatalf("final state = %q, want %q", got, c.wantFinal)
			}
		})
	}
}

func TestLedgerResendsWhenReconcilerConfirmsNotApplied(t *testing.T) {
	store, meter := tempStore(t), newMeter()
	inner := reconcilingMeter{fakeMeter: meter}
	ledger := adapter.NewLedger(store, inner)

	meter.loseReplies = 1
	if _, err := ledger.Execute(context.Background(), kwhCredit); !errors.Is(err, adapter.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	// Forget the application so the device reports the reference as never seen.
	meter.mu.Lock()
	meter.applied = nil
	meter.mu.Unlock()
	if _, err := ledger.Execute(context.Background(), kwhCredit); err != nil {
		t.Fatalf("retry after reconciler reported not applied: %v", err)
	}
	if n := meter.appliedCount(kwhCredit.ID); n != 1 {
		t.Fatalf("credit applied %d times after reconciliation, want once", n)
	}
}

func TestLedgerReportsUnknownWhenReconcilerFails(t *testing.T) {
	store, meter := tempStore(t), newMeter()
	ledger := adapter.NewLedger(store, reconcilingMeter{fakeMeter: meter, outcomeErr: errNoReply})
	meter.loseReplies = 1
	_, _ = ledger.Execute(context.Background(), disconnect)
	_, err := ledger.Execute(context.Background(), disconnect)
	if !errors.Is(err, adapter.ErrOutcomeUnknown) || !errors.Is(err, errNoReply) {
		t.Fatalf("error = %v, want ErrOutcomeUnknown wrapping the reconciler failure", err)
	}
	if n := meter.appliedCount(disconnect.ID); n != 1 {
		t.Fatalf("command resent %d times while its outcome could not be established", n-1)
	}
}

func TestLedgerRejectionIsFinal(t *testing.T) {
	store, meter := tempStore(t), newMeter()
	meter.reject = true
	ledger := adapter.NewLedger(store, meter)

	if _, err := ledger.Execute(context.Background(), kwhCredit); !errors.Is(err, adapter.ErrDeviceRejected) {
		t.Fatalf("error = %v, want ErrDeviceRejected", err)
	}
	meter.mu.Lock()
	meter.reject = false
	meter.mu.Unlock()
	_, err := ledger.Execute(context.Background(), kwhCredit)
	if !errors.Is(err, adapter.ErrDeviceRejected) || !strings.Contains(err.Error(), "E_METER_LOCKED") {
		t.Fatalf("retry error = %v, want the recorded rejection", err)
	}
	if strings.Count(err.Error(), adapter.ErrDeviceRejected.Error()) != 1 {
		t.Fatalf("recorded rejection repeats its prefix: %v", err)
	}
	if n := meter.appliedCount(kwhCredit.ID); n != 0 {
		t.Fatalf("rejected command applied %d times on retry", n)
	}
}

func TestLedgerRejectsReusedIDWithDifferentParameters(t *testing.T) {
	store, meter := tempStore(t), newMeter()
	ledger := adapter.NewLedger(store, meter)
	if _, err := ledger.Execute(context.Background(), kwhCredit); err != nil {
		t.Fatal(err)
	}
	larger := kwhCredit
	larger.CreditKWh = 125
	if _, err := ledger.Execute(context.Background(), larger); !errors.Is(err, adapter.ErrInvalidCommand) {
		t.Fatalf("error = %v, want ErrInvalidCommand", err)
	}
	if n := meter.appliedCount(kwhCredit.ID); n != 1 {
		t.Fatalf("command applied %d times, want once", n)
	}
}

type intentLossStore struct{ *queuesqlite.Store }

func (intentLossStore) BeginCommandAttempt(context.Context, adapter.LedgerEntry, time.Time) error {
	return errors.New("disk full")
}

func TestLedgerDoesNotSendWhenIntentCannotBeRecorded(t *testing.T) {
	store, meter := tempStore(t), newMeter()
	_, err := adapter.NewLedger(intentLossStore{store}, meter).Execute(context.Background(), kwhCredit)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("error = %v, want the store failure", err)
	}
	if n := meter.appliedCount(kwhCredit.ID); n != 0 {
		t.Fatalf("command sent %d times without a durable intent", n)
	}
}

func TestLedgerRecordsOutcomeAfterCallerGivesUp(t *testing.T) {
	store, meter := tempStore(t), newMeter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	meter.afterApply = cancel
	if _, err := adapter.NewLedger(store, meter).Execute(ctx, kwhCredit); err != nil {
		t.Fatal(err)
	}
	if got := state(t, store, kwhCredit.ID); got != adapter.CommandApplied {
		t.Fatalf("state = %q, want applied despite the cancelled caller", got)
	}
}

func TestLedgerLeavesNoEntryWhenNothingWasSent(t *testing.T) {
	store, meter := tempStore(t), newMeter()
	queued := fmt.Errorf("%w: %w", adapter.ErrTimeout, context.DeadlineExceeded)
	meter.failBefore = queued
	ledger := adapter.NewLedger(store, meter)

	if _, err := ledger.Execute(context.Background(), kwhCredit); !errors.Is(err, adapter.ErrTimeout) || errors.Is(err, adapter.ErrOutcomeUnknown) {
		t.Fatalf("error = %v, want ErrTimeout without ErrOutcomeUnknown", err)
	}
	if got := state(t, store, kwhCredit.ID); got != "" {
		t.Fatalf("unsent command left a %q entry", got)
	}
	meter.mu.Lock()
	meter.failBefore = nil
	meter.mu.Unlock()
	if _, err := ledger.Execute(context.Background(), kwhCredit); err != nil {
		t.Fatalf("retry of an unsent command: %v", err)
	}
	if n := meter.appliedCount(kwhCredit.ID); n != 1 {
		t.Fatalf("credit applied %d times, want once", n)
	}
}

func TestLedgerRecordsSuccessReportedWithoutBeforeSend(t *testing.T) {
	store, meter := tempStore(t), newMeter()
	meter.skipHook = true
	ledger := adapter.NewLedger(store, meter)

	if _, err := ledger.Execute(context.Background(), kwhCredit); !errors.Is(err, adapter.ErrSendHookSkipped) {
		t.Fatalf("error = %v, want ErrSendHookSkipped", err)
	}
	if got := state(t, store, kwhCredit.ID); got != adapter.CommandApplied {
		t.Fatalf("state = %q, want applied so a retry is not resent", got)
	}
	result, err := ledger.Execute(context.Background(), kwhCredit)
	if err != nil || result.CommandID != kwhCredit.ID {
		t.Fatalf("retry = %+v, %v; want the recorded result", result, err)
	}
	if n := meter.appliedCount(kwhCredit.ID); n != 1 {
		t.Fatalf("credit applied %d times, want once", n)
	}
}

// unavailableStore stands in for a store that cannot write while down is set.
type unavailableStore struct {
	*queuesqlite.Store
	down *atomic.Bool
}

func (s unavailableStore) BeginCommandAttempt(ctx context.Context, entry adapter.LedgerEntry, at time.Time) error {
	if s.down.Load() {
		return errors.New("disk I/O error")
	}
	return s.Store.BeginCommandAttempt(ctx, entry, at)
}

func TestLedgerNeverResendsAnUnhookedCommandItCouldNotRecord(t *testing.T) {
	sqlite, meter := tempStore(t), newMeter()
	meter.skipHook = true
	var down atomic.Bool
	down.Store(true)
	ledger := adapter.NewLedger(unavailableStore{sqlite, &down}, meter)

	if _, err := ledger.Execute(context.Background(), kwhCredit); !errors.Is(err, adapter.ErrSendHookSkipped) {
		t.Fatalf("error = %v, want ErrSendHookSkipped", err)
	}
	result, _ := ledger.Execute(context.Background(), kwhCredit)
	if result.CommandID != kwhCredit.ID {
		t.Fatalf("retry while the store is down = %+v, want the applied result", result)
	}
	if n := meter.appliedCount(kwhCredit.ID); n != 1 {
		t.Fatalf("credit applied %d times while unrecorded, want once", n)
	}

	down.Store(false)
	if result, err := ledger.Execute(context.Background(), kwhCredit); err != nil || result.CommandID != kwhCredit.ID {
		t.Fatalf("retry after recovery = %+v, %v", result, err)
	}
	if got := state(t, sqlite, kwhCredit.ID); got != adapter.CommandApplied {
		t.Fatalf("state after recovery = %q, want applied", got)
	}
	if n := meter.appliedCount(kwhCredit.ID); n != 1 {
		t.Fatalf("credit applied %d times, want once", n)
	}
}
