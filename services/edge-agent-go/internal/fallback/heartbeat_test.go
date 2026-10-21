package fallback_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"edge-agent-go/internal/fallback"
)

func TestFormatHeartbeatMatchesSharedContract(t *testing.T) {
	var contract struct {
		Message   string `json:"message"`
		Heartbeat struct {
			SiteID          string `json:"site_id"`
			GatewayID       string `json:"gateway_id"`
			Sequence        uint64 `json:"sequence"`
			IntervalSeconds int64  `json:"interval_seconds"`
			SentAtUnix      int64  `json:"sent_at_unix"`
		} `json:"heartbeat"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "proto", "fixtures", "sms_heartbeat_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	got, err := fallback.FormatHeartbeat(fallback.Heartbeat{
		SiteID: contract.Heartbeat.SiteID, GatewayID: contract.Heartbeat.GatewayID,
		Sequence: contract.Heartbeat.Sequence, Interval: time.Duration(contract.Heartbeat.IntervalSeconds) * time.Second,
		SentAt: time.Unix(contract.Heartbeat.SentAtUnix, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != contract.Message {
		t.Fatalf("FormatHeartbeat() = %q, shared contract = %q", got, contract.Message)
	}
}

type fakeHeartbeatStore struct {
	state    fallback.HeartbeatState
	sequence uint64
	records  map[uint64]string
}

func (s *fakeHeartbeatStore) HeartbeatState(context.Context, time.Time) (fallback.HeartbeatState, error) {
	return s.state, nil
}

func (s *fakeHeartbeatStore) ReserveHeartbeat(_ context.Context, at time.Time) (uint64, error) {
	s.sequence++
	s.state.LastAttemptAt = &at
	s.state.LastAttemptFailed = false
	s.state.AttemptsLastDay++
	return s.sequence, nil
}

func (s *fakeHeartbeatStore) RecordHeartbeat(_ context.Context, sequence uint64, failureReason string) error {
	if s.records == nil {
		s.records = map[uint64]string{}
	}
	s.records[sequence] = failureReason
	s.state.LastAttemptFailed = failureReason != ""
	return nil
}

type scriptedSender struct {
	messages []string
	fail     bool
}

func (s *scriptedSender) Send(_ context.Context, _, message string) error {
	s.messages = append(s.messages, message)
	if s.fail {
		return errors.New("modem unavailable")
	}
	return nil
}

func newHeartbeatCoordinator(store *fakeHeartbeatStore, sender fallback.Sender, clock *time.Time) *fallback.Coordinator {
	coordinator := fallback.NewCoordinator(fallback.Config{
		Destination: "+2348000000000", FailureThreshold: 3, OfflineAfter: 2 * time.Minute,
		MaxAttempts: 3, HourlyLimit: 6, DailyLimit: 20, RetryInterval: 7*time.Minute + 30*time.Second,
	}, &fakeStore{}, sender)
	coordinator.EnableHeartbeat(fallback.HeartbeatConfig{
		SiteID: "ng-kaji-01", GatewayID: "gateway-01", Interval: 30 * time.Minute, DailyLimit: 3,
	}, store)
	coordinator.SetClock(func() time.Time { return *clock })
	return coordinator
}

func heartbeatCount(sender *scriptedSender) int {
	count := 0
	for _, message := range sender.messages {
		if strings.Contains(message, "|kind=HB|") {
			count++
		}
	}
	return count
}

func TestCoordinatorSendsHeartbeatsOnlyWhilePacketDataIsDown(t *testing.T) {
	start := time.Unix(1730000000, 0).UTC()
	clock := start
	store := &fakeHeartbeatStore{state: fallback.HeartbeatState{LastUploadSuccess: ptrTime(start)}}
	sender := &scriptedSender{}
	coordinator := newHeartbeatCoordinator(store, sender, &clock)
	run := func(at time.Time) {
		t.Helper()
		clock = at
		if err := coordinator.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce() error = %v", err)
		}
	}

	run(start.Add(time.Minute))
	if got := heartbeatCount(sender); got != 0 {
		t.Fatalf("heartbeats while uploads are healthy = %d, want 0", got)
	}

	run(start.Add(2 * time.Minute))
	if got := heartbeatCount(sender); got != 1 {
		t.Fatalf("heartbeats after going offline = %d, want 1", got)
	}
	if !strings.HasSuffix(sender.messages[0], "|seq=1|int=1800|ts="+strconv.FormatInt(start.Add(2*time.Minute).Unix(), 10)) {
		t.Fatalf("heartbeat message = %q", sender.messages[0])
	}

	run(start.Add(20 * time.Minute))
	if got := heartbeatCount(sender); got != 1 {
		t.Fatalf("heartbeats inside the interval = %d, want 1", got)
	}

	run(start.Add(32 * time.Minute))
	if got := heartbeatCount(sender); got != 2 {
		t.Fatalf("heartbeats after one interval = %d, want 2", got)
	}

	recoveredAt := start.Add(70 * time.Minute)
	lastUpload := recoveredAt.Add(-5 * time.Second)
	store.state.LastUploadSuccess = &lastUpload
	run(recoveredAt)
	if got := heartbeatCount(sender); got != 2 {
		t.Fatalf("heartbeats after HTTP recovery = %d, want 2", got)
	}
}

func TestCoordinatorSendsImmediatelyWhenPacketDataFailsAgain(t *testing.T) {
	start := time.Unix(1730000000, 0).UTC()
	clock := start
	store := &fakeHeartbeatStore{state: fallback.HeartbeatState{LastUploadSuccess: ptrTime(start)}}
	sender := &scriptedSender{}
	coordinator := newHeartbeatCoordinator(store, sender, &clock)
	run := func(at time.Time) {
		t.Helper()
		clock = at
		if err := coordinator.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce() error = %v", err)
		}
	}

	// First outage opens and sends its heartbeat.
	run(start.Add(2 * time.Minute))
	if got := heartbeatCount(sender); got != 1 {
		t.Fatalf("heartbeats after first outage = %d, want 1", got)
	}

	// HTTP recovers before the normal 30-minute heartbeat interval expires.
	recoveredAt := start.Add(3 * time.Minute)
	store.state.LastUploadSuccess = &recoveredAt
	run(recoveredAt)

	// A later outage gets its first heartbeat as soon as OfflineAfter elapses;
	// the attempt belonging to the first outage must not throttle it.
	run(recoveredAt.Add(2 * time.Minute))
	if got := heartbeatCount(sender); got != 2 {
		t.Fatalf("heartbeats after second outage = %d, want 2", got)
	}
}

func TestCoordinatorHeartbeatUsesItsOwnDailyBudget(t *testing.T) {
	start := time.Unix(1730000000, 0).UTC()
	clock := start
	store := &fakeHeartbeatStore{state: fallback.HeartbeatState{AttemptsLastDay: 3}}
	sender := &scriptedSender{}
	coordinator := newHeartbeatCoordinator(store, sender, &clock)

	clock = start.Add(time.Hour)
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock = start.Add(2 * time.Hour)
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := heartbeatCount(sender); got != 0 {
		t.Fatalf("heartbeats over the daily limit = %d, want 0", got)
	}
}

func TestCoordinatorRetriesFailedHeartbeatSooner(t *testing.T) {
	start := time.Unix(1730000000, 0).UTC()
	clock := start
	store := &fakeHeartbeatStore{}
	sender := &scriptedSender{fail: true}
	coordinator := newHeartbeatCoordinator(store, sender, &clock)

	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock = start.Add(3 * time.Minute)
	if err := coordinator.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() hid the heartbeat send failure")
	}
	if store.records[1] == "" {
		t.Fatal("failed heartbeat was not recorded with a reason")
	}
	sender.fail = false
	clock = start.Add(3*time.Minute + 7*time.Minute + 30*time.Second)
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := heartbeatCount(sender); got != 2 || store.records[2] != "" {
		t.Fatalf("heartbeats = %d records = %v, want a successful retry after the retry interval", got, store.records)
	}
}

func TestCoordinatorSendsCriticalIncidentBeforeHeartbeat(t *testing.T) {
	start := time.Unix(1730000000, 0).UTC()
	clock := start.Add(5 * time.Minute)
	sender := &scriptedSender{}
	coordinator := fallback.NewCoordinator(fallback.Config{
		Destination: "+2348000000000", FailureThreshold: 3, OfflineAfter: 2 * time.Minute,
		MaxAttempts: 3, HourlyLimit: 6, DailyLimit: 20, RetryInterval: 7*time.Minute + 30*time.Second,
	}, &fakeStore{candidates: []fallback.Candidate{{
		Event: fallback.Event{
			SiteID: "ng-kaji-01", AssetID: "site", Sequence: 9, Code: fallback.SiteOutage, OccurredAt: start,
		},
		OpenedAt: start,
	}}}, sender)
	coordinator.EnableHeartbeat(fallback.HeartbeatConfig{
		SiteID: "ng-kaji-01", GatewayID: "gateway-01", Interval: 30 * time.Minute, DailyLimit: 48,
	}, &fakeHeartbeatStore{state: fallback.HeartbeatState{LastUploadSuccess: ptrTime(start)}})
	coordinator.SetClock(func() time.Time { return clock })

	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 || !strings.Contains(sender.messages[0], "code=SITE_OUTAGE") || !strings.Contains(sender.messages[1], "kind=HB") {
		t.Fatalf("messages = %q, want critical incident then heartbeat", sender.messages)
	}
}
