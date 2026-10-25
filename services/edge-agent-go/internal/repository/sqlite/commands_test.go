package sqlite_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

var commandAt = time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)

func commandEntry(id string) adapter.LedgerEntry {
	hash := sha256.Sum256([]byte(id))
	return adapter.LedgerEntry{CommandID: id, DeviceID: "meter-01", Type: adapter.CommandCredit, RequestHash: hash[:]}
}

func openCommandStore(t *testing.T, path string) *queuesqlite.Store {
	t.Helper()
	store, err := queuesqlite.Open(context.Background(), testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func loadCommand(t *testing.T, store *queuesqlite.Store, id string) adapter.LedgerEntry {
	t.Helper()
	entry, found, err := store.LoadCommand(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("load command %q: found %t, error %v", id, found, err)
	}
	return entry
}

func TestCommandLedgerRecordsAppliedResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openCommandStore(t, filepath.Join(t.TempDir(), "queue.db"))
	defer store.Close()

	if _, found, err := store.LoadCommand(ctx, "cmd-1"); err != nil || found {
		t.Fatalf("unknown command: found %t, error %v", found, err)
	}
	entry := commandEntry("cmd-1")
	if err := store.BeginCommandAttempt(ctx, entry, commandAt); err != nil {
		t.Fatal(err)
	}
	if got := loadCommand(t, store, "cmd-1"); got.State != adapter.CommandInFlight || got.Attempts != 1 || !got.AcceptedAt.Equal(commandAt) {
		t.Fatalf("begun command = %+v", got)
	}

	balance, relay := 12.5, true
	result := adapter.CommandResult{CommandID: "cmd-1", AcknowledgedAt: commandAt.Add(time.Second), BalanceKWh: &balance, RelayClosed: &relay}
	if err := store.CompleteCommand(ctx, "cmd-1", adapter.CommandApplied, &result, "", commandAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := loadCommand(t, store, "cmd-1")
	if got.State != adapter.CommandApplied || got.Result == nil || !reflect.DeepEqual(*got.Result, result) {
		t.Fatalf("applied command = %+v, result %+v", got, got.Result)
	}
	if !bytes.Equal(got.RequestHash, entry.RequestHash) || got.Type != adapter.CommandCredit || got.DeviceID != "meter-01" {
		t.Fatalf("applied command identity = %+v", got)
	}
	if err := store.BeginCommandAttempt(ctx, entry, commandAt.Add(time.Minute)); !errors.Is(err, adapter.ErrInvalidCommand) {
		t.Fatalf("restarting an applied command error = %v, want ErrInvalidCommand", err)
	}
	if err := store.MarkCommandUnknown(ctx, "cmd-1", "late timeout", commandAt.Add(time.Minute)); err == nil {
		t.Fatal("an applied command was marked unknown")
	}
}

func TestCommandLedgerRetriesOnlyUnresolvedMatchingRequests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openCommandStore(t, filepath.Join(t.TempDir(), "queue.db"))
	defer store.Close()

	entry := commandEntry("cmd-1")
	if err := store.BeginCommandAttempt(ctx, entry, commandAt); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCommandUnknown(ctx, "cmd-1", "adapter operation exceeded its deadline", commandAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	changed := entry
	changed.RequestHash = commandEntry("other").RequestHash
	if err := store.BeginCommandAttempt(ctx, changed, commandAt.Add(time.Minute)); !errors.Is(err, adapter.ErrInvalidCommand) {
		t.Fatalf("retry with different parameters error = %v, want ErrInvalidCommand", err)
	}
	if err := store.BeginCommandAttempt(ctx, entry, commandAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got := loadCommand(t, store, "cmd-1")
	if got.State != adapter.CommandInFlight || got.Attempts != 2 || !got.AcceptedAt.Equal(commandAt) || !got.UpdatedAt.Equal(commandAt.Add(time.Minute)) {
		t.Fatalf("retried command = %+v", got)
	}

	if err := store.CompleteCommand(ctx, "cmd-1", adapter.CommandRejected, nil, "device rejected the command: E_METER_LOCKED", commandAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := loadCommand(t, store, "cmd-1"); got.State != adapter.CommandRejected || got.Result != nil || got.LastError == "" {
		t.Fatalf("rejected command = %+v", got)
	}
	if err := store.CompleteCommand(ctx, "cmd-1", adapter.CommandUnknown, nil, "", commandAt); err == nil {
		t.Fatal("unknown was accepted as a terminal state")
	}
}

func TestCommandLedgerMarksInterruptedCommandsUnknownOnOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store := openCommandStore(t, path)
	if err := store.BeginCommandAttempt(ctx, commandEntry("cmd-1"), commandAt); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openCommandStore(t, path)
	defer store.Close()
	got := loadCommand(t, store, "cmd-1")
	if got.State != adapter.CommandUnknown || got.LastError == "" {
		t.Fatalf("interrupted command after reopen = %+v", got)
	}
	stats, err := store.CommandLedgerStats(ctx, commandAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Unresolved != 1 || stats.OldestUnresolvedAge != time.Hour {
		t.Fatalf("ledger stats = %+v, want one unresolved command an hour old", stats)
	}
}

func TestCommandLedgerPrunesOnlyOldResolvedCommands(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openCommandStore(t, filepath.Join(t.TempDir(), "queue.db"))
	defer store.Close()

	old, recent := commandAt, commandAt.Add(30*24*time.Hour)
	result := adapter.CommandResult{CommandID: "x", AcknowledgedAt: old}
	for _, c := range []struct {
		id    string
		at    time.Time
		state adapter.CommandState
	}{
		{"old-applied-1", old, adapter.CommandApplied},
		{"old-applied-2", old.Add(time.Second), adapter.CommandApplied},
		{"old-rejected", old.Add(2 * time.Second), adapter.CommandRejected},
		{"old-unknown", old, adapter.CommandUnknown},
		{"old-in-flight", old, adapter.CommandInFlight},
		{"recent-applied", recent, adapter.CommandApplied},
	} {
		if err := store.BeginCommandAttempt(ctx, commandEntry(c.id), c.at); err != nil {
			t.Fatal(err)
		}
		var err error
		switch c.state {
		case adapter.CommandApplied:
			err = store.CompleteCommand(ctx, c.id, c.state, &result, "", c.at)
		case adapter.CommandRejected:
			err = store.CompleteCommand(ctx, c.id, c.state, nil, "rejected", c.at)
		case adapter.CommandUnknown:
			err = store.MarkCommandUnknown(ctx, c.id, "timed out", c.at)
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	cutoff := recent.Add(-time.Hour)
	if deleted, err := store.PruneCommands(ctx, cutoff, 2); err != nil || deleted != 2 {
		t.Fatalf("first batch deleted %d, error %v; want 2", deleted, err)
	}
	if deleted, err := store.PruneCommands(ctx, cutoff, 2); err != nil || deleted != 1 {
		t.Fatalf("second batch deleted %d, error %v; want 1", deleted, err)
	}
	for _, id := range []string{"old-applied-1", "old-applied-2", "old-rejected"} {
		if _, found, _ := store.LoadCommand(ctx, id); found {
			t.Errorf("%s survived pruning", id)
		}
	}
	for _, id := range []string{"old-unknown", "old-in-flight", "recent-applied"} {
		loadCommand(t, store, id)
	}
}

func TestOpenMigratesVersionNineQueueWithCommandLedger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store := openCommandStore(t, path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE command_reports; DROP TABLE command_ledger; PRAGMA user_version = 9;`); err != nil {
		t.Fatalf("downgrade to version nine: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store = openCommandStore(t, path)
	defer store.Close()
	if err := store.BeginCommandAttempt(ctx, commandEntry("cmd-1"), commandAt); err != nil {
		t.Fatalf("use command ledger after migration: %v", err)
	}
}

// reportStatuses reads the outbox directly, so it neither delivers nor
// reorders the reports a test is about to inspect.
func reportStatuses(t *testing.T, path, commandID string) []domain.CommandReportStatus {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT status FROM command_reports WHERE command_id = ? ORDER BY id", commandID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var statuses []domain.CommandReportStatus
	for rows.Next() {
		var status domain.CommandReportStatus
		if err := rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func TestCommandLedgerQueuesReportsWithEachTransition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store := openCommandStore(t, path)
	defer store.Close()

	ackAt := commandAt.Add(3 * time.Second)
	for id, finish := range map[string]func(string) error{
		"applied": func(id string) error {
			return store.CompleteCommand(ctx, id, adapter.CommandApplied, &adapter.CommandResult{CommandID: id, AcknowledgedAt: ackAt}, "", commandAt.Add(5*time.Second))
		},
		"rejected": func(id string) error {
			return store.CompleteCommand(ctx, id, adapter.CommandRejected, nil, "device rejected the command: E_METER_LOCKED", commandAt)
		},
		"unknown": func(id string) error { return store.MarkCommandUnknown(ctx, id, "timed out", commandAt) },
	} {
		if err := store.BeginCommandAttempt(ctx, commandEntry(id), commandAt); err != nil {
			t.Fatal(err)
		}
		if err := finish(id); err != nil {
			t.Fatal(err)
		}
	}

	want := map[string][]domain.CommandReportStatus{
		"applied":  {domain.ReportSent, domain.ReportAcknowledged},
		"rejected": {domain.ReportSent, domain.ReportFailed},
		"unknown":  {domain.ReportSent},
	}
	for id, statuses := range want {
		if got := reportStatuses(t, path, id); !reflect.DeepEqual(got, statuses) {
			t.Errorf("%s reports = %v, want %v", id, got, statuses)
		}
	}
	reports, err := store.DueCommandReports(ctx, commandAt.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reports {
		if r.CommandID == "applied" && r.Status == domain.ReportAcknowledged && !r.OccurredAt.Equal(ackAt) {
			t.Errorf("acknowledgement reported at %s, want the device's %s", r.OccurredAt, ackAt)
		}
		if r.CommandID == "rejected" && r.Status == domain.ReportFailed && r.FailureReason == "" {
			t.Error("failure report has no reason")
		}
	}

	// A failed write must not leave a report for a transition that never happened.
	if err := store.BeginCommandAttempt(ctx, commandEntry("applied"), commandAt); err == nil {
		t.Fatal("restarted an applied command")
	}
	if got := reportStatuses(t, path, "applied"); len(got) != 2 {
		t.Fatalf("refused transition queued a report: %v", got)
	}
}

func TestCommandLedgerRefusesOnlyCommandsItHasNotSeen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store := openCommandStore(t, path)
	defer store.Close()

	refused, err := store.RefuseCommand(ctx, commandEntry("late"), domain.ReportExpired, "expired before delivery", commandAt)
	if err != nil || !refused {
		t.Fatalf("refuse unseen command = %t, %v", refused, err)
	}
	if got := loadCommand(t, store, "late"); got.State != adapter.CommandRejected || got.CompletedAt.IsZero() {
		t.Fatalf("refused entry = %+v", got)
	}
	if got := reportStatuses(t, path, "late"); !reflect.DeepEqual(got, []domain.CommandReportStatus{domain.ReportExpired}) {
		t.Fatalf("refusal reports = %v", got)
	}
	if again, err := store.RefuseCommand(ctx, commandEntry("late"), domain.ReportExpired, "expired", commandAt); err != nil || again {
		t.Fatalf("second refusal = %t, %v; want a no-op", again, err)
	}

	if err := store.BeginCommandAttempt(ctx, commandEntry("maybe-applied"), commandAt); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCommandUnknown(ctx, "maybe-applied", "no reply", commandAt); err != nil {
		t.Fatal(err)
	}
	if refused, err := store.RefuseCommand(ctx, commandEntry("maybe-applied"), domain.ReportExpired, "expired", commandAt); err != nil || refused {
		t.Fatalf("refusing a command that may have reached the device = %t, %v", refused, err)
	}
	if got := reportStatuses(t, path, "maybe-applied"); !reflect.DeepEqual(got, []domain.CommandReportStatus{domain.ReportSent}) {
		t.Fatalf("unknown command reports = %v, want only sent", got)
	}
	if _, err := store.RefuseCommand(ctx, commandEntry("x"), domain.ReportAcknowledged, "", commandAt); err == nil {
		t.Fatal("accepted acknowledged as a refusal")
	}
}

func TestCommandLedgerFindsNewestRelayCommandThatMayHaveActed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openCommandStore(t, filepath.Join(t.TempDir(), "queue.db"))
	defer store.Close()

	relay := func(id string, typ adapter.CommandType, requested time.Time) adapter.LedgerEntry {
		e := commandEntry(id)
		e.Type, e.RequestedAt = typ, requested
		return e
	}
	if _, _, found, err := store.LastRelayCommand(ctx, "meter-01"); err != nil || found {
		t.Fatalf("empty ledger found %t, %v", found, err)
	}
	for _, e := range []adapter.LedgerEntry{
		relay("off-early", adapter.CommandDisconnect, commandAt),
		relay("on-later", adapter.CommandReconnect, commandAt.Add(time.Minute)),
	} {
		if err := store.BeginCommandAttempt(ctx, e, commandAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RefuseCommand(ctx, relay("off-newest-refused", adapter.CommandDisconnect, commandAt.Add(time.Hour)), domain.ReportFailed, "superseded", commandAt); err != nil {
		t.Fatal(err)
	}
	status := commandEntry("status-newest")
	status.Type, status.RequestedAt = adapter.CommandRequestStatus, commandAt.Add(2*time.Hour)
	if err := store.BeginCommandAttempt(ctx, status, commandAt); err != nil {
		t.Fatal(err)
	}

	requested, id, found, err := store.LastRelayCommand(ctx, "meter-01")
	if err != nil || !found || id != "on-later" || !requested.Equal(commandAt.Add(time.Minute)) {
		t.Fatalf("last relay = %q at %s (found %t, %v); want on-later", id, requested, found, err)
	}
}

func TestCommandReportsRetryDeferAndAbandon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openCommandStore(t, filepath.Join(t.TempDir(), "queue.db"))
	defer store.Close()

	for _, id := range []string{"a", "b", "c"} {
		if err := store.BeginCommandAttempt(ctx, commandEntry(id), commandAt); err != nil {
			t.Fatal(err)
		}
	}
	due, err := store.DueCommandReports(ctx, commandAt, 10)
	if err != nil || len(due) != 3 || due[0].CommandID != "a" {
		t.Fatalf("due reports = %+v, %v", due, err)
	}
	if err := store.DeliveredCommandReport(ctx, due[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeferCommandReport(ctx, due[1].ID, commandAt.Add(time.Minute), "connection refused"); err != nil {
		t.Fatal(err)
	}
	if err := store.AbandonCommandReport(ctx, due[2].ID, commandAt, "409 conflict"); err != nil {
		t.Fatal(err)
	}
	if now, err := store.DueCommandReports(ctx, commandAt, 10); err != nil || len(now) != 0 {
		t.Fatalf("due now = %+v, %v; want none", now, err)
	}
	later, err := store.DueCommandReports(ctx, commandAt.Add(time.Minute), 10)
	if err != nil || len(later) != 1 || later[0].CommandID != "b" || later[0].Attempts != 1 {
		t.Fatalf("due after deferral = %+v, %v", later, err)
	}
	stats, err := store.CommandLedgerStats(ctx, commandAt)
	if err != nil || stats.PendingReports != 1 || stats.AbandonedReports != 1 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	if _, err := store.PruneCommands(ctx, commandAt.Add(time.Second), 10); err != nil {
		t.Fatal(err)
	}
	if stats, err := store.CommandLedgerStats(ctx, commandAt); err != nil || stats.AbandonedReports != 0 || stats.PendingReports != 1 {
		t.Fatalf("stats after prune = %+v, %v", stats, err)
	}
}

func TestCommandReportsLeaveInOrderPerCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openCommandStore(t, filepath.Join(t.TempDir(), "queue.db"))
	defer store.Close()

	for _, id := range []string{"a", "b"} {
		if err := store.BeginCommandAttempt(ctx, commandEntry(id), commandAt); err != nil {
			t.Fatal(err)
		}
	}
	result := adapter.CommandResult{CommandID: "a", AcknowledgedAt: commandAt}
	if err := store.CompleteCommand(ctx, "a", adapter.CommandApplied, &result, "", commandAt); err != nil {
		t.Fatal(err)
	}
	due := func() []string {
		t.Helper()
		reports, err := store.DueCommandReports(ctx, commandAt.Add(time.Hour), 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range reports {
			out = append(out, r.CommandID+":"+string(r.Status))
		}
		return out
	}
	if got := due(); !reflect.DeepEqual(got, []string{"a:sent", "b:sent"}) {
		t.Fatalf("due = %v, want each command's first report only", got)
	}
	first, err := store.DueCommandReports(ctx, commandAt, 1)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	if err := store.DeferCommandReport(ctx, first[0].ID, commandAt.Add(30*time.Minute), "unavailable"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.DueCommandReports(ctx, commandAt, 10); err != nil || len(got) != 1 || got[0].CommandID != "b" {
		t.Fatalf("due while a:sent is deferred = %+v, %v; want only b", got, err)
	}
	if err := store.DeliveredCommandReport(ctx, first[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := due(); !reflect.DeepEqual(got, []string{"b:sent", "a:acknowledged"}) {
		t.Fatalf("due after a:sent delivered = %v", got)
	}
}
