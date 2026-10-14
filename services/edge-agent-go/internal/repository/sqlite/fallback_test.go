package sqlite_test

import (
	"context"
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
	if err := store.OpenFallbackIncident(ctx, event, now); err != nil {
		t.Fatalf("OpenFallbackIncident() error = %v", err)
	}
	duplicate := event
	duplicate.Sequence = 11
	if err := store.OpenFallbackIncident(ctx, duplicate, now); err != nil {
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
	if err := store.OpenFallbackIncident(ctx, event, now); err != nil {
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
		if err := store.OpenFallbackIncident(ctx, fallbackEvent, now); err != nil {
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
		if err := store.OpenFallbackIncident(ctx, fallback.Event{SiteID: "site-01", AssetID: "batt-01", Sequence: sequence, Code: fallback.BatteryOverheat, TemperatureC: &temperature, OccurredAt: at}, at); err != nil {
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

func ptrFloat(value float64) *float64 { return &value }
