package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/fallback"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

func TestFallbackIncidentSurvivesRestartAndSuppressesDuplicates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Unix(1730000200, 0).UTC()
	event := fallback.Event{
		SiteID: "site-01", AssetID: "battery-01", Sequence: 10,
		Code: fallback.BatteryOverheat, TemperatureC: ptrFloat(68.2),
		OccurredAt: now.Add(-time.Minute),
	}
	if opened, err := store.OpenFallbackIncident(ctx, event, now); err != nil || !opened {
		t.Fatalf("OpenFallbackIncident() error = %v", err)
	}
	duplicate := event
	duplicate.Sequence = 11
	if opened, err := store.OpenFallbackIncident(ctx, duplicate, now); err != nil || opened {
		t.Fatalf("duplicate OpenFallbackIncident() error = %v", err)
	}
	if err := store.RecordFallbackUploadFailure(ctx, event.Sequence); err != nil {
		t.Fatalf("RecordFallbackUploadFailure() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer store.Close()
	candidates, err := store.FallbackCandidates(ctx, now)
	if err != nil {
		t.Fatalf("FallbackCandidates() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Sequence != 10 || candidates[0].UploadFailures != 1 {
		t.Fatalf("candidates = %+v, want original durable incident", candidates)
	}
}

func TestSuccessfulNormalUploadRemovesFallbackCandidate(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, testConfig(filepath.Join(t.TempDir(), "queue.db")))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	now := time.Unix(1730000200, 0).UTC()
	event := fallback.Event{
		SiteID: "site-01", AssetID: "inv-01", Sequence: 12,
		Code: fallback.InverterFailure, OccurredAt: now.Add(-time.Minute),
	}
	if _, err := store.OpenFallbackIncident(ctx, event, now); err != nil {
		t.Fatalf("OpenFallbackIncident() error = %v", err)
	}
	if err := store.RecordFallbackUploadSuccess(ctx, event.Sequence, now); err != nil {
		t.Fatalf("RecordFallbackUploadSuccess() error = %v", err)
	}
	candidates, err := store.FallbackCandidates(ctx, now)
	if err != nil {
		t.Fatalf("FallbackCandidates() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want none after normal delivery", candidates)
	}
}

func TestFallbackRateLimitStatsReportsBlockedIncident(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, testConfig(filepath.Join(t.TempDir(), "queue.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1730000200, 0).UTC()
	event := fallback.Event{SiteID: "site-01", AssetID: "inv-01", Sequence: 12, Code: fallback.InverterFailure, OccurredAt: now.Add(-time.Minute)}
	if _, err := store.OpenFallbackIncident(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		attemptedAt := now.Add(time.Duration(i) * time.Second)
		if err := store.ReserveFallbackAttempt(ctx, event.EventKey(), attemptedAt, 7*time.Minute+30*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordFallbackAttempt(ctx, fallback.AttemptResult{EventKey: event.EventKey(), AttemptedAt: attemptedAt, FailureReason: "modem unavailable"}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := store.FallbackRateLimitStats(ctx, now.Add(3*time.Second), domain.FallbackRateLimitPolicy{MaxAttempts: 3, HourlyLimit: 3, DailyLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if stats.IncidentAttemptLimit != 1 || stats.SiteHourlyLimit != 1 || stats.SiteDailyLimit != 1 {
		t.Fatalf("rate-limit stats = %+v, want incident reported for all exhausted limits", stats)
	}
}

func TestRestartDoesNotReopenSuppressedHistoricalIncident(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1730000200, 0).UTC()
	for i := 0; i < 2; i++ {
		temperature := 68.2
		event := domain.Event{DeviceID: "batt-01", AssetType: domain.AssetBatteryBMS, MQTTTopic: "site/batt", Payload: []byte("reading"), Priority: domain.PriorityCritical, EventTimestamp: now.Add(time.Duration(i) * time.Second), EdgeReceivedAt: now, CriticalCode: string(fallback.BatteryOverheat), CriticalValue: &temperature}
		sequence, err := store.Persist(ctx, event)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkPendingUpload(ctx, sequence); err != nil {
			t.Fatal(err)
		}
		fallbackEvent := fallback.Event{SiteID: "site-01", AssetID: "batt-01", Sequence: sequence, Code: fallback.BatteryOverheat, TemperatureC: &temperature, OccurredAt: event.EventTimestamp}
		if _, err := store.OpenFallbackIncident(ctx, fallbackEvent, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ResolveFallbackIncident(ctx, "batt-01", fallback.BatteryOverheat, now.Add(time.Minute)); err != nil {
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
	candidates, err := store.FallbackCandidates(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want historical duplicates suppressed", candidates)
	}
}

func TestRestartPreservesOpeningMetadataAcrossRecurrences(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	open := func(at time.Time) uint64 {
		temperature := 68.2
		event := domain.Event{DeviceID: "batt-01", AssetType: domain.AssetBatteryBMS, MQTTTopic: "site/batt", Payload: []byte("reading"), Priority: domain.PriorityCritical, EventTimestamp: at, EdgeReceivedAt: at, CriticalCode: string(fallback.BatteryOverheat), CriticalValue: &temperature}
		sequence, err := store.Persist(ctx, event)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkPendingUpload(ctx, sequence); err != nil {
			t.Fatal(err)
		}
		if _, err := store.OpenFallbackIncident(ctx, fallback.Event{SiteID: "site-01", AssetID: "batt-01", Sequence: sequence, Code: fallback.BatteryOverheat, TemperatureC: &temperature, OccurredAt: at}, at); err != nil {
			t.Fatal(err)
		}
		return sequence
	}
	first := open(now)
	if err := store.ResolveFallbackIncident(ctx, "batt-01", fallback.BatteryOverheat, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	_ = open(now.Add(2 * time.Minute))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Record(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if record.CriticalCode != string(fallback.BatteryOverheat) {
		t.Fatalf("critical code = %q, want preserved", record.CriticalCode)
	}
}

func TestReservationAtomicallySetsRetryDelay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := queuesqlite.Open(ctx, testConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1730000200, 0).UTC()
	event := fallback.Event{
		SiteID: "site-01", AssetID: "inv-01", Sequence: 20,
		Code: fallback.InverterFailure, OccurredAt: now.Add(-time.Minute),
	}
	if _, err := store.OpenFallbackIncident(ctx, event, now); err != nil {
		t.Fatal(err)
	}

	retryInterval := 7*time.Minute + 30*time.Second
	if err := store.ReserveFallbackAttempt(ctx, event.EventKey(), now, retryInterval); err != nil {
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

	candidates, err := store.FallbackCandidates(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	if candidates[0].NextAttemptAt == nil {
		t.Fatal("reservation did not set next_sms_attempt_at atomically")
	}
	wantNext := now.Add(retryInterval)
	if !candidates[0].NextAttemptAt.Equal(wantNext) {
		t.Fatalf("next attempt = %v, want %v", candidates[0].NextAttemptAt, wantNext)
	}
	stats, err := store.FallbackRateLimitStats(ctx, now.Add(time.Second), domain.FallbackRateLimitPolicy{
		MaxAttempts: 1, HourlyLimit: 6, DailyLimit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.IncidentAttemptLimit != 1 {
		t.Fatalf("incident attempt limit count = %d, want 1 after reserved attempt", stats.IncidentAttemptLimit)
	}
}

func TestReservationOnlyUpdatesItsOwnIncidentRetryDelay(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, testConfig(filepath.Join(t.TempDir(), "queue.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Unix(1730000200, 0).UTC()
	first := fallback.Event{SiteID: "site-01", AssetID: "inv-01", Sequence: 30, Code: fallback.InverterFailure, OccurredAt: now}
	second := fallback.Event{SiteID: "site-01", AssetID: "inv-02", Sequence: 31, Code: fallback.InverterFailure, OccurredAt: now}
	for _, event := range []fallback.Event{first, second} {
		if _, err := store.OpenFallbackIncident(ctx, event, now); err != nil {
			t.Fatal(err)
		}
	}

	secondRetryAt := now.Add(time.Minute)
	if err := store.ReserveFallbackAttempt(ctx, second.EventKey(), now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveFallbackAttempt(ctx, first.EventKey(), now.Add(2*time.Minute), 7*time.Minute+30*time.Second); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.FallbackCandidates(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.EventKey() == second.EventKey() {
			if candidate.NextAttemptAt == nil || !candidate.NextAttemptAt.Equal(secondRetryAt) {
				t.Fatalf("second incident retry = %v, want %v", candidate.NextAttemptAt, secondRetryAt)
			}
			return
		}
	}
	t.Fatal("second incident candidate not found")
}

func TestReservationRetryDelayBlocksImmediateReattempt(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, testConfig(filepath.Join(t.TempDir(), "queue.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1730000200, 0).UTC()
	event := fallback.Event{
		SiteID: "site-01", AssetID: "inv-01", Sequence: 21,
		Code: fallback.InverterFailure, OccurredAt: now.Add(-time.Minute),
	}
	if _, err := store.OpenFallbackIncident(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	retryInterval := 7*time.Minute + 30*time.Second
	if err := store.ReserveFallbackAttempt(ctx, event.EventKey(), now, retryInterval); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFallbackAttempt(ctx, fallback.AttemptResult{
		EventKey: event.EventKey(), AttemptedAt: now, FailureReason: "modem unavailable",
	}); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.FallbackCandidates(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	if candidates[0].NextAttemptAt == nil || !candidates[0].NextAttemptAt.After(now.Add(time.Second)) {
		t.Fatalf("retry delay should block immediate reattempt, next = %v", candidates[0].NextAttemptAt)
	}
}

type failingSender struct {
	attempts int
}

func (s *failingSender) Send(context.Context, string, string) error {
	s.attempts++
	return errors.New("modem unavailable")
}

func TestCoordinatorSpreadsThreeAttemptsAcrossFifteenMinutes(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, testConfig(filepath.Join(t.TempDir(), "queue.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	start := time.Unix(1730000200, 0).UTC()
	event := fallback.Event{
		SiteID: "site-01", AssetID: "inv-01", Sequence: 40,
		Code: fallback.InverterFailure, OccurredAt: start.Add(-time.Minute),
	}
	if _, err := store.OpenFallbackIncident(ctx, event, start); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := store.RecordFallbackUploadFailure(ctx, event.Sequence); err != nil {
			t.Fatal(err)
		}
	}

	sender := &failingSender{}
	clock := start
	coordinator := fallback.NewCoordinator(fallback.Config{
		Destination: "+2348000000000", FailureThreshold: 3, OfflineAfter: 2 * time.Minute,
		MaxAttempts: 3, HourlyLimit: 6, DailyLimit: 20,
		RetryInterval: 7*time.Minute + 30*time.Second,
	}, store, sender)
	coordinator.SetClock(func() time.Time { return clock })

	check := func(at time.Duration, wantAttempts int) {
		t.Helper()
		clock = start.Add(at)
		before := sender.attempts
		err := coordinator.RunOnce(ctx)
		if sender.attempts != wantAttempts {
			t.Fatalf("attempts at %v = %d, want %d", at, sender.attempts, wantAttempts)
		}
		if sender.attempts > before && err == nil {
			t.Fatalf("RunOnce(%v) error = nil after failed modem send", at)
		}
		if sender.attempts == before && err != nil {
			t.Fatalf("RunOnce(%v) unexpected error without send: %v", at, err)
		}
	}

	check(0, 1)
	check(time.Minute, 1)
	check(7*time.Minute+29*time.Second, 1)
	check(7*time.Minute+30*time.Second, 2)
	check(14*time.Minute+59*time.Second, 2)
	check(15*time.Minute, 3)
	check(30*time.Minute, 3)
}

func ptrFloat(value float64) *float64 { return &value }
