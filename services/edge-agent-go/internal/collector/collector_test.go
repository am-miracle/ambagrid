package collector_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"edge-agent-go/internal/adapter/simulator"
	"edge-agent-go/internal/collector"
	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/fallback"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

func TestSimulatedReadingsAreNormalizedAndQueued(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, queuesqlite.Config{
		Path:                   filepath.Join(t.TempDir(), "queue.db"),
		SiteID:                 "ng-kaji-01",
		GatewayID:              "gateway-01",
		MaxStorageBytes:        8 << 20,
		WarningPercent:         70,
		CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	defer store.Close()

	c, err := collector.New(collector.Config{
		SiteID: "ng-kaji-01", Region: "africa-west", Interval: time.Second,
	}, store, simulator.New())
	if err != nil {
		t.Fatalf("create collector: %v", err)
	}
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatalf("collect telemetry: %v", err)
	}

	records, err := store.Ready(ctx, 10, 1<<20)
	if err != nil {
		t.Fatalf("read queued telemetry: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("queued records = %d, want meter, battery, and inverter", len(records))
	}
	wantTypes := []domain.AssetType{domain.AssetSmartMeter, domain.AssetBatteryBMS, domain.AssetSolarInverter}
	for i, record := range records {
		if record.Sequence != uint64(i+1) {
			t.Errorf("record %d sequence = %d, want %d", i, record.Sequence, i+1)
		}
		if record.SiteID != "ng-kaji-01" || record.AssetType != wantTypes[i] {
			t.Errorf("record %d identity = site %q type %q", i, record.SiteID, record.AssetType)
		}
		if record.Status != domain.StatusPendingUpload {
			t.Errorf("record %d status = %q, want %q", i, record.Status, domain.StatusPendingUpload)
		}
		if record.EventTimestamp.IsZero() || record.EdgeReceivedAt.IsZero() {
			t.Errorf("record %d timestamps were not captured", i)
		}
		var payload domain.TelemetryPayload
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Errorf("decode record %d payload: %v", i, err)
			continue
		}
		if payload.SiteID != record.SiteID || payload.DeviceID != record.DeviceID || payload.TimestampUTC != record.EventTimestamp.Unix() {
			t.Errorf("record %d payload identity/timestamp does not match envelope: %#v", i, payload)
		}
	}
	if records[0].MQTTTopic != "africa-west/ng-kaji-01/smartmeter/meter-01/telemetry" {
		t.Fatalf("meter topic = %q", records[0].MQTTTopic)
	}
}

func TestBatteryOverheatIsQueuedAsCriticalFallback(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, queuesqlite.Config{
		Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "ng-kaji-01", GatewayID: "gateway-01",
		MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1730000000, 0).UTC()
	source := fixedSource{readings: []domain.Reading{{
		DeviceID: "batt-01", AssetType: domain.AssetBatteryBMS, TakenAt: now, InternalTemperature: 68.2,
	}}}
	c, err := collector.New(collector.Config{SiteID: "ng-kaji-01", Region: "africa-west", Interval: time.Second, BatteryOverheatC: 55}, store, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := store.Ready(ctx, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Priority != domain.PriorityCritical || records[0].CriticalCode != string(fallback.BatteryOverheat) {
		t.Fatalf("record = %+v, want critical battery overheat", records)
	}
	candidates, err := store.FallbackCandidates(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].EventKey() != "edge:ng-kaji-01:1" {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestIndependentCriticalConditionsGetIndependentSequences(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, queuesqlite.Config{Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "site-01", GatewayID: "gateway-01", MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1730000000, 0).UTC()
	source := fixedSource{readings: []domain.Reading{{DeviceID: "inv-01", AssetType: domain.AssetSolarInverter, TakenAt: now, InverterFailed: true, TamperDetected: true}}}
	c, err := collector.New(collector.Config{SiteID: "site-01", Region: "africa-west", Interval: time.Second}, store, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := store.Ready(ctx, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].CriticalCode != string(fallback.InverterFailure) {
		t.Fatalf("ready records = %+v, first device sequence should preserve ordering", records)
	}
	candidates, err := store.FallbackCandidates(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].Sequence == candidates[1].Sequence {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestCriticalIncidentIsDurableBeforeRecordBecomesUploadable(t *testing.T) {
	queue := &orderedQueue{}
	now := time.Unix(1730000000, 0).UTC()
	source := fixedSource{readings: []domain.Reading{{
		DeviceID: "batt-01", AssetType: domain.AssetBatteryBMS, TakenAt: now, InternalTemperature: 68.2,
	}}}
	c, err := collector.New(collector.Config{
		SiteID: "site-01", Region: "africa-west", Interval: time.Second, BatteryOverheatC: 55,
	}, queue, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := queue.steps; len(got) != 3 || got[0] != "persist" || got[1] != "incident" || got[2] != "pending" {
		t.Fatalf("steps = %v, want persist, incident, pending", got)
	}
}

type orderedQueue struct{ steps []string }

func (q *orderedQueue) Persist(context.Context, domain.Event) (uint64, error) {
	q.steps = append(q.steps, "persist")
	return 1, nil
}
func (q *orderedQueue) OpenFallbackIncident(context.Context, fallback.Event, time.Time) error {
	q.steps = append(q.steps, "incident")
	return nil
}
func (q *orderedQueue) MarkPendingUpload(context.Context, uint64) error {
	q.steps = append(q.steps, "pending")
	return nil
}
func (*orderedQueue) ResolveFallbackIncident(context.Context, string, fallback.Code, time.Time) error {
	return nil
}
func (*orderedQueue) Ready(context.Context, int, int64) ([]domain.Record, error)  { return nil, nil }
func (*orderedQueue) MarkUploaded(context.Context, uint64, time.Time) error       { return nil }
func (*orderedQueue) Ack(context.Context, uint64) error                           { return nil }
func (*orderedQueue) MarkFailed(context.Context, uint64, time.Time, string) error { return nil }
func (*orderedQueue) Stats(context.Context) (domain.QueueStats, error) {
	return domain.QueueStats{}, nil
}

type fixedSource struct{ readings []domain.Reading }

func (s fixedSource) Read(context.Context) ([]domain.Reading, error) { return s.readings, nil }
