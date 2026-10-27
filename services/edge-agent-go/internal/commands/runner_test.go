package commands_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/simulator"
	"edge-agent-go/internal/commands"
	"edge-agent-go/internal/domain"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

const siteKey = "key-kaji"

type wireReport struct {
	CommandID     string
	Status        string    `json:"status"`
	OccurredAt    time.Time `json:"occurred_at"`
	FailureReason string    `json:"failure_reason"`
}

// fakePlatform keeps pending commands until it receives a terminal report,
// like api-go, and can fail report delivery on demand.
type fakePlatform struct {
	mu            sync.Mutex
	pending       map[string]commands.Pending
	reports       []wireReport
	reportFailure int
	// clockAhead shifts the Date header, standing in for a gateway clock that
	// is behind the platform's by this much.
	clockAhead time.Duration
}

func newPlatform(t *testing.T, pending ...commands.Pending) (*fakePlatform, *httptest.Server) {
	p := &fakePlatform{pending: make(map[string]commands.Pending)}
	for _, c := range pending {
		p.pending[c.CommandID] = c
	}
	server := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(server.Close)
	return p, server
}

func (p *fakePlatform) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+siteKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Date", time.Now().Add(p.clockAhead).UTC().Format(http.TimeFormat))
	if r.URL.Path == "/v1/edge/commands/poll" {
		data := make([]commands.Pending, 0, len(p.pending))
		for _, c := range p.pending {
			data = append(data, c)
		}
		// Newest first, so the runner's own ordering is what the tests observe.
		slices.SortFunc(data, func(a, b commands.Pending) int { return b.RequestedAt.Compare(a.RequestedAt) })
		json.NewEncoder(w).Encode(map[string]any{"data": data})
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/edge/commands/"), "/report")
	if p.reportFailure != 0 {
		w.WriteHeader(p.reportFailure)
		return
	}
	var report wireReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	report.CommandID = id
	p.reports = append(p.reports, report)
	if report.Status != "sent" {
		delete(p.pending, id)
	}
	json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"command_id": id, "status": report.Status}})
}

func (p *fakePlatform) statuses(commandID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.reports {
		if r.CommandID == commandID {
			out = append(out, r.Status)
		}
	}
	return out
}

func (p *fakePlatform) add(c commands.Pending) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending[c.CommandID] = c
}

type harness struct {
	runner *commands.Runner
	store  *queuesqlite.Store
	meter  *simulator.Adapter
}

func newHarness(t *testing.T, server *httptest.Server) harness {
	t.Helper()
	store, err := queuesqlite.Open(context.Background(), queuesqlite.Config{
		Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "ng-kaji-01", GatewayID: "gateway-01",
		MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	meter := simulator.New("ng-kaji-01")
	runner := commands.New(
		commands.Config{PollInterval: time.Hour, CommandTimeout: 100 * time.Millisecond},
		commands.NewHTTPClient(server.URL, siteKey, time.Second),
		adapter.NewLedger(store, meter), store,
	)
	return harness{runner: runner, store: store, meter: meter}
}

func pending(id, commandType string, requestedAgo, ttl time.Duration) commands.Pending {
	requested := time.Now().UTC().Add(-requestedAgo)
	return commands.Pending{
		CommandID: id, MeterID: "met-0101", DeviceID: "meter-01", CommandType: commandType,
		Status: "queued", RequestedAt: requested, ExpiresAt: requested.Add(ttl),
	}
}

func (h harness) sync(t *testing.T) {
	t.Helper()
	if err := h.runner.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

func (h harness) relayClosed(t *testing.T) bool {
	t.Helper()
	readings, err := h.meter.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range readings {
		if r.DeviceID == "meter-01" {
			return r.RelayClosed
		}
	}
	t.Fatal("no meter reading")
	return false
}

func TestRunnerDeliversCommandAndReportsBothSteps(t *testing.T) {
	platform, server := newPlatform(t, pending("cmd-off", "disconnect_meter", time.Minute, time.Hour))
	h := newHarness(t, server)
	h.sync(t)

	if h.relayClosed(t) {
		t.Fatal("meter relay still closed after disconnect")
	}
	if got := platform.statuses("cmd-off"); !reflect.DeepEqual(got, []string{"sent", "acknowledged"}) {
		t.Fatalf("reports = %v, want sent then acknowledged", got)
	}
	h.sync(t)
	if n := len(h.meter.Device().Received()); n != 1 {
		t.Fatalf("meter received %d commands after a second sync, want 1", n)
	}
}

func TestRunnerRefusesExpiredCommandWithoutSendingIt(t *testing.T) {
	platform, server := newPlatform(t, pending("cmd-late", "disconnect_meter", 2*time.Hour, time.Hour))
	h := newHarness(t, server)
	h.sync(t)

	if !h.relayClosed(t) || len(h.meter.Device().Received()) != 0 {
		t.Fatal("an expired command reached the meter")
	}
	if got := platform.statuses("cmd-late"); !reflect.DeepEqual(got, []string{"expired"}) {
		t.Fatalf("reports = %v, want expired", got)
	}
}

func TestRunnerRefusesRelayCommandOlderThanOneAlreadySent(t *testing.T) {
	platform, server := newPlatform(t, pending("cmd-on-new", "reconnect_meter", time.Minute, time.Hour))
	h := newHarness(t, server)
	h.sync(t)

	// A delayed retry of an older disconnect arrives after the reconnect ran.
	platform.add(pending("cmd-off-old", "disconnect_meter", 10*time.Minute, time.Hour))
	h.sync(t)

	if !h.relayClosed(t) {
		t.Fatal("stale disconnect opened the relay after a newer reconnect")
	}
	if got := platform.statuses("cmd-off-old"); !reflect.DeepEqual(got, []string{"failed"}) {
		t.Fatalf("stale command reports = %v, want failed", got)
	}
	platform.mu.Lock()
	reason := platform.reports[len(platform.reports)-1].FailureReason
	platform.mu.Unlock()
	if !strings.Contains(reason, "superseded") || !strings.Contains(reason, "cmd-on-new") {
		t.Fatalf("failure reason = %q", reason)
	}
}

func TestRunnerAppliesRelayCommandsInRequestOrderWithinOnePoll(t *testing.T) {
	platform, server := newPlatform(t,
		pending("cmd-z-on", "reconnect_meter", time.Minute, time.Hour),
		pending("cmd-a-off", "disconnect_meter", 2*time.Minute, time.Hour),
	)
	h := newHarness(t, server)
	h.sync(t)

	if !h.relayClosed(t) {
		t.Fatal("relay open: the older disconnect ran after the newer reconnect")
	}
	for _, id := range []string{"cmd-a-off", "cmd-z-on"} {
		if got := platform.statuses(id); !reflect.DeepEqual(got, []string{"sent", "acknowledged"}) {
			t.Errorf("%s reports = %v", id, got)
		}
	}
}

func TestRunnerDeliversCreditAmountToTheMeterOnce(t *testing.T) {
	credit := pending("cmd-credit", "credit_meter", time.Minute, time.Hour)
	credit.CreditKWh = 12.5
	platform, server := newPlatform(t, credit)
	h := newHarness(t, server)
	h.sync(t)
	h.sync(t)

	status, err := h.meter.Execute(context.Background(), adapter.Command{ID: "status", Type: adapter.CommandRequestStatus, DeviceID: "meter-01"})
	if err != nil {
		t.Fatal(err)
	}
	if status.BalanceKWh == nil || *status.BalanceKWh != 12.5 {
		t.Fatalf("meter balance = %v, want 12.5 kWh", status.BalanceKWh)
	}
	if got := platform.statuses("cmd-credit"); !reflect.DeepEqual(got, []string{"sent", "acknowledged"}) {
		t.Fatalf("reports = %v", got)
	}
}

func TestRunnerRefusesCommandsTheMeterCannotRun(t *testing.T) {
	platform, server := newPlatform(t,
		pending("cmd-credit", "credit_meter", time.Minute, time.Hour),
		pending("cmd-reboot", "reboot_meter", time.Minute, time.Hour),
	)
	h := newHarness(t, server)
	h.sync(t)

	if n := len(h.meter.Device().Received()); n != 0 {
		t.Fatalf("meter received %d commands", n)
	}
	for _, id := range []string{"cmd-credit", "cmd-reboot"} {
		if got := platform.statuses(id); !reflect.DeepEqual(got, []string{"failed"}) {
			t.Errorf("%s reports = %v, want failed", id, got)
		}
	}
}

func TestRunnerLeavesUnconfirmedCommandAtSentAndRetriesSafeOnes(t *testing.T) {
	platform, server := newPlatform(t, pending("cmd-off", "disconnect_meter", time.Minute, time.Hour))
	h := newHarness(t, server)
	h.meter.Device().SetMeterOnline(false)
	h.sync(t)

	if got := platform.statuses("cmd-off"); !reflect.DeepEqual(got, []string{"sent"}) {
		t.Fatalf("reports while meter offline = %v, want only sent", got)
	}
	entry, _, err := h.store.LoadCommand(context.Background(), "cmd-off")
	if err != nil || entry.State != adapter.CommandUnknown {
		t.Fatalf("ledger entry = %+v, %v; want unknown", entry, err)
	}

	h.meter.Device().SetMeterOnline(true)
	h.sync(t)
	if h.relayClosed(t) {
		t.Fatal("disconnect was not resent once the meter came back")
	}
	if got := platform.statuses("cmd-off"); !reflect.DeepEqual(got, []string{"sent", "sent", "acknowledged"}) {
		t.Fatalf("reports = %v", got)
	}
}

func TestRunnerKeepsReportsUntilThePlatformTakesThem(t *testing.T) {
	platform, server := newPlatform(t, pending("cmd-off", "disconnect_meter", time.Minute, time.Hour))
	h := newHarness(t, server)
	platform.mu.Lock()
	platform.reportFailure = http.StatusServiceUnavailable
	platform.mu.Unlock()

	if err := h.runner.SyncOnce(context.Background()); err == nil {
		t.Fatal("sync succeeded while reports could not be delivered")
	}
	if n := len(h.meter.Device().Received()); n != 1 {
		t.Fatalf("meter received %d commands, want 1", n)
	}
	stats, err := h.store.CommandLedgerStats(context.Background(), time.Now())
	if err != nil || stats.PendingReports != 2 {
		t.Fatalf("pending reports = %+v, %v; want sent and acknowledged queued", stats, err)
	}

	platform.mu.Lock()
	platform.reportFailure = 0
	platform.mu.Unlock()
	h.sync(t)
	if got := platform.statuses("cmd-off"); len(got) != 0 {
		t.Fatalf("reports delivered before their retry time: %v", got)
	}

	h.runner.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	h.sync(t)
	if got := platform.statuses("cmd-off"); !reflect.DeepEqual(got, []string{"sent", "acknowledged"}) {
		t.Fatalf("reports after recovery = %v, want sent then acknowledged", got)
	}
	if stats, err := h.store.CommandLedgerStats(context.Background(), time.Now()); err != nil || stats.PendingReports != 0 {
		t.Fatalf("pending reports after delivery = %+v, %v", stats, err)
	}
}

func TestRunnerDeliversReportsAcceptedForReconciliation(t *testing.T) {
	platform, server := newPlatform(t, pending("cmd-off", "disconnect_meter", time.Minute, time.Hour))
	platform.reportFailure = http.StatusAccepted
	h := newHarness(t, server)
	h.sync(t)
	stats, err := h.store.CommandLedgerStats(context.Background(), time.Now())
	if err != nil || stats.PendingReports != 0 || stats.AbandonedReports != 0 {
		t.Fatalf("accepted evidence must leave neither pending nor abandoned reports: %+v err=%v", stats, err)
	}
	h.sync(t)
	if n := len(h.meter.Device().Received()); n != 1 {
		t.Fatalf("accepted reports caused the command to run %d times", n)
	}
}

func TestRunnerAbandonsReportsThePlatformRefuses(t *testing.T) {
	platform, server := newPlatform(t, pending("cmd-off", "disconnect_meter", time.Minute, time.Hour))
	h := newHarness(t, server)
	platform.mu.Lock()
	platform.reportFailure = http.StatusConflict
	platform.mu.Unlock()

	h.sync(t)
	stats, err := h.store.CommandLedgerStats(context.Background(), time.Now())
	if err != nil || stats.PendingReports != 0 || stats.AbandonedReports != 2 {
		t.Fatalf("stats = %+v, %v; want both reports abandoned, none pending", stats, err)
	}
}

// stuckStore returns the same report however it is handled.
type stuckStore struct {
	commands.Store
	report domain.CommandReport
}

func (s stuckStore) DueCommandReports(context.Context, time.Time, int) ([]domain.CommandReport, error) {
	return []domain.CommandReport{s.report}, nil
}

func (stuckStore) AbandonCommandReport(context.Context, int64, time.Time, string) error { return nil }

func TestRunnerStopsWhenTheStoreKeepsReturningAHandledReport(t *testing.T) {
	platform, server := newPlatform(t)
	platform.reportFailure = http.StatusConflict
	h := newHarness(t, server)
	store := stuckStore{Store: h.store, report: domain.CommandReport{ID: 7, CommandID: "cmd-x", Status: domain.ReportSent, OccurredAt: time.Now()}}
	runner := commands.New(commands.Config{PollInterval: time.Hour, CommandTimeout: time.Second},
		commands.NewHTTPClient(server.URL, siteKey, time.Second), adapter.NewLedger(h.store, h.meter), store)

	done := make(chan error, 1)
	go func() { done <- runner.SyncOnce(context.Background()) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "still due") {
			t.Fatalf("sync error = %v, want the repeated report reported", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("report delivery kept looping on a report the store never released")
	}
}

func expiresIn(p commands.Pending, d time.Duration) commands.Pending {
	ms := d.Milliseconds()
	p.ExpiresInMS = &ms
	return p
}

func TestRunnerTrustsPlatformTimeRemainingOverItsOwnClock(t *testing.T) {
	// To a gateway clock two days fast, the absolute deadline is long gone, but
	// the platform measured an hour left when it handed the command over.
	live := expiresIn(pending("cmd-live", "disconnect_meter", 49*time.Hour, 48*time.Hour), time.Hour)
	// To a gateway clock two days slow, this one looks far off, but the platform
	// says no time is left.
	late := expiresIn(pending("cmd-late", "reconnect_meter", -47*time.Hour, 48*time.Hour), 0)
	platform, server := newPlatform(t, live, late)
	h := newHarness(t, server)
	h.sync(t)

	if got := platform.statuses("cmd-live"); !reflect.DeepEqual(got, []string{"sent", "acknowledged"}) {
		t.Errorf("live command reports = %v, want it run", got)
	}
	if got := platform.statuses("cmd-late"); !reflect.DeepEqual(got, []string{"expired"}) {
		t.Errorf("late command reports = %v, want it refused", got)
	}
}

func TestRunnerIgnoresOverdueRedeliveryOfACommandItAlreadyRan(t *testing.T) {
	platform, server := newPlatform(t, pending("cmd-ran", "disconnect_meter", time.Minute, time.Hour))
	platform.reportFailure = http.StatusServiceUnavailable
	h := newHarness(t, server)
	_ = h.runner.SyncOnce(context.Background())

	// Its reports have not landed, so the platform still has it queued and
	// redelivers it past its deadline.
	platform.mu.Lock()
	platform.reportFailure = 0
	platform.mu.Unlock()
	platform.add(expiresIn(pending("cmd-ran", "disconnect_meter", 2*time.Hour, time.Hour), -time.Hour))
	_ = h.runner.SyncOnce(context.Background())

	if got := len(h.meter.Device().Received()); got != 1 {
		t.Fatalf("meter received %d commands, want the command sent once", got)
	}
	if got := platform.statuses("cmd-ran"); slices.Contains(got, "expired") {
		t.Fatalf("reports = %v; a command that ran must never be reported expired", got)
	}
}

func TestClientCorrectsReportTimesForGatewayClockOffset(t *testing.T) {
	for name, c := range map[string]struct {
		ahead, wantShift time.Duration
	}{
		"gateway an hour slow":  {ahead: time.Hour, wantShift: time.Hour},
		"within Date precision": {ahead: time.Second, wantShift: 0},
	} {
		t.Run(name, func(t *testing.T) {
			platform, server := newPlatform(t)
			platform.clockAhead = c.ahead
			client := commands.NewHTTPClient(server.URL, siteKey, time.Second)
			if _, err := client.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			occurred := time.Now().UTC()
			if err := client.Report(context.Background(), domain.CommandReport{CommandID: "cmd-x", Status: domain.ReportSent, OccurredAt: occurred}); err != nil {
				t.Fatal(err)
			}
			platform.mu.Lock()
			got := platform.reports[0].OccurredAt
			platform.mu.Unlock()
			// The Date header has one-second resolution.
			if shift := got.Sub(occurred); (shift - c.wantShift).Abs() > 1500*time.Millisecond {
				t.Fatalf("reported time shifted by %s, want about %s", shift, c.wantShift)
			}
		})
	}
}

type retentionMetrics struct{ exceeded []bool }

func (m *retentionMetrics) ExpiryBeyondRetention(exceeded bool) {
	m.exceeded = append(m.exceeded, exceeded)
}

func TestRunnerFlagsCommandsThatCanOutliveLedgerRetention(t *testing.T) {
	platform, server := newPlatform(t, pending("cmd-long", "disconnect_meter", 0, 2*time.Hour))
	h := newHarness(t, server)
	metrics := &retentionMetrics{}
	runner := commands.New(
		commands.Config{CommandTimeout: time.Second, Retention: time.Hour, Metrics: metrics},
		commands.NewHTTPClient(server.URL, siteKey, time.Second), adapter.NewLedger(h.store, h.meter), h.store,
	)
	if err := runner.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := platform.statuses("cmd-long"); !reflect.DeepEqual(got, []string{"sent", "acknowledged"}) {
		t.Fatalf("reports = %v, want the command still run", got)
	}
	platform.add(pending("cmd-short", "reconnect_meter", 0, 30*time.Minute))
	if err := runner.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(metrics.exceeded, []bool{true, false}) {
		t.Fatalf("expiry beyond retention = %v, want flagged then cleared", metrics.exceeded)
	}
}

// deadlineExecutor observes the public runner/executor boundary without a real
// transport delay: the supplied deadline must never outlive platform expiry.
type deadlineExecutor struct{ deadline time.Time }

func (e *deadlineExecutor) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }
func (e *deadlineExecutor) Execute(ctx context.Context, _ adapter.Command) (adapter.CommandResult, error) {
	e.deadline, _ = ctx.Deadline()
	return adapter.CommandResult{}, nil
}
func TestExecutionDeadlineCannotOutliveCommand(t *testing.T) {
	_, server := newPlatform(t, pending("cmd-expiring", "disconnect_meter", 0, 2*time.Second))
	h := newHarness(t, server)
	executor := &deadlineExecutor{}
	runner := commands.New(commands.Config{CommandTimeout: time.Hour}, commands.NewHTTPClient(server.URL, siteKey, time.Second), executor, h.store)
	before := time.Now()
	if err := runner.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if executor.deadline.IsZero() || executor.deadline.After(before.Add(2100*time.Millisecond)) {
		t.Fatalf("execution deadline %s exceeds command expiry", executor.deadline)
	}
}

func TestRunnerOrdersRelaysWithSubmillisecondTimestamp(t *testing.T) {
	for _, tc := range []struct {
		name, newerID, olderID string
		difference             time.Duration
		stale                  bool
	}{
		{"timestamp before ID", "cmd-a-on", "cmd-z-off", time.Nanosecond, true},
		{"equal timestamp uses ID", "cmd-z-on", "cmd-a-off", 0, true},
		{"later command still executes", "cmd-z-on", "cmd-a-off", -time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requested := time.Now().UTC().Add(-time.Minute).Truncate(time.Second).Add(123456789 * time.Nanosecond)
			newer := pending(tc.newerID, "reconnect_meter", time.Minute, time.Hour)
			newer.RequestedAt = requested.Add(tc.difference)
			platform, server := newPlatform(t, newer)
			h := newHarness(t, server)
			h.sync(t)
			older := pending(tc.olderID, "disconnect_meter", time.Minute, time.Hour)
			older.RequestedAt = requested
			platform.add(older)
			h.sync(t)
			wantReports := []string{"failed"}
			wantSends := 1
			if !tc.stale {
				wantReports = []string{"sent", "acknowledged"}
				wantSends = 2
			}
			if h.relayClosed(t) != tc.stale || len(h.meter.Device().Received()) != wantSends {
				t.Fatalf("relay ordering failed: stale=%t, sends=%d", tc.stale, len(h.meter.Device().Received()))
			}
			if got := platform.statuses(tc.olderID); !reflect.DeepEqual(got, wantReports) {
				t.Fatalf("command reports = %v, want %v", got, wantReports)
			}
		})
	}
}
