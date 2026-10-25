package collector_test

import (
	"context"
	"encoding/json"
	"errors"
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
	}, store, simulator.New("ng-kaji-01"))
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
		var payload map[string]interface{}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Errorf("decode record %d payload: %v", i, err)
			continue
		}
		if payload["site_id"] != record.SiteID || payload["device_id"] != record.DeviceID {
			t.Errorf("record %d payload identity does not match envelope", i)
		}
		if int64(payload["timestamp_utc"].(float64)) != record.EventTimestamp.Unix() {
			t.Errorf("record %d payload timestamp does not match envelope", i)
		}
	}
	if records[0].MQTTTopic != "africa-west/ng-kaji-01/smartmeter/meter-01/telemetry" {
		t.Fatalf("meter topic = %q", records[0].MQTTTopic)
	}
}

func TestAdapterPayloadBytesArePreservedExactly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, queuesqlite.Config{
		Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "site-01", GatewayID: "gateway-01",
		MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Unix(1730000000, 0).UTC()
	original := []byte(`{  "device_id":"meter-01", "unknown_field": true, "timestamp_utc": 1730000000,  "site_id":"site-01"}`)
	source := fixedSource{readings: []domain.Reading{{
		DeviceID: "meter-01", AssetType: domain.AssetSmartMeter, TakenAt: now,
		Payload: original,
	}}}
	c, err := collector.New(collector.Config{SiteID: "site-01", Region: "africa-west", Interval: time.Second}, store, source)
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
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if string(records[0].Payload) != string(original) {
		t.Fatalf("payload was reshaped:\n  got:  %s\n  want: %s", records[0].Payload, original)
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
		Payload: testPayload("batt-01", "ng-kaji-01", now),
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

func TestInverterFailureRequiresLocalDebounce(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, queuesqlite.Config{
		Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "site-01", GatewayID: "gateway-01",
		MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1730000000, 0).UTC()
	source := fixedSource{readings: []domain.Reading{{
		DeviceID: "inv-01", AssetType: domain.AssetSolarInverter, TakenAt: now, InverterFailed: true,
		Payload: testPayload("inv-01", "site-01", now),
	}}}
	c, err := collector.New(collector.Config{SiteID: "site-01", Region: "africa-west", Interval: time.Second}, store, source)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	c.SetClock(func() time.Time { return clock })
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := store.Ready(ctx, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Priority != domain.PriorityNormal || records[0].CriticalCode != "" {
		t.Fatalf("first active sample = %+v, want ordinary telemetry until debounce completes", records)
	}
	candidates, err := store.FallbackCandidates(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("first active sample opened fallback candidates: %+v", candidates)
	}
	clock = clock.Add(30 * time.Second)
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	candidates, err = store.FallbackCandidates(ctx, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Code != fallback.InverterFailure {
		t.Fatalf("sustained failure candidates = %+v, want one inverter incident", candidates)
	}
	clock = clock.Add(30 * time.Second)
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	candidates, err = store.FallbackCandidates(ctx, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("continuous failure candidates = %+v, want original incident reused", candidates)
	}
}

func TestInverterHardFaultBypassesDebounce(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, queuesqlite.Config{
		Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "site-01", GatewayID: "gateway-01",
		MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1730000000, 0).UTC()
	source := fixedSource{readings: []domain.Reading{{
		DeviceID: "inv-01", AssetType: domain.AssetSolarInverter, TakenAt: now, InverterHardFault: true,
		Payload: testPayload("inv-01", "site-01", now),
	}}}
	c, err := collector.New(collector.Config{SiteID: "site-01", Region: "africa-west", Interval: time.Second}, store, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.FallbackCandidates(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Code != fallback.InverterFailure {
		t.Fatalf("hard fault candidates = %+v, want immediate inverter incident without debounce", candidates)
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
	source := fixedSource{readings: []domain.Reading{{DeviceID: "inv-01", AssetType: domain.AssetSolarInverter, TakenAt: now, InverterFailed: true, TamperDetected: true, Payload: testPayload("inv-01", "site-01", now)}}}
	c, err := collector.New(collector.Config{SiteID: "site-01", Region: "africa-west", Interval: time.Second}, store, source)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	c.SetClock(func() time.Time { return clock })
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
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
		Payload: testPayload("batt-01", "site-01", now),
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
	if got := queue.steps; len(got) != 4 || got[0] != "debounce" || got[1] != "persist" || got[2] != "incident" || got[3] != "pending" {
		t.Fatalf("steps = %v, want debounce, persist, incident, pending", got)
	}
}

func TestDebounceStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, queuesqlite.Config{
		Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "site-01", GatewayID: "gateway-01",
		MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Unix(1730000000, 0).UTC()
	source := fixedSource{readings: []domain.Reading{{
		DeviceID: "inv-01", AssetType: domain.AssetSolarInverter, TakenAt: now, InverterFailed: true,
		Payload: testPayload("inv-01", "site-01", now),
	}}}

	c1, err := collector.New(collector.Config{SiteID: "site-01", Region: "africa-west", Interval: time.Second}, store, source)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	c1.SetClock(func() time.Time { return clock })

	if err := c1.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.FallbackCandidates(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatal("incident should not open during debounce")
	}

	clock = clock.Add(20 * time.Second)

	c2, err := collector.New(collector.Config{SiteID: "site-01", Region: "africa-west", Interval: time.Second}, store, source)
	if err != nil {
		t.Fatal(err)
	}
	c2.SetClock(func() time.Time { return clock })
	if err := c2.RestoreDebounceState(ctx); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(10 * time.Second)
	if err := c2.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}

	candidates, err = store.FallbackCandidates(ctx, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Code != fallback.InverterFailure {
		t.Fatalf("debounce should have continued across restart; candidates = %+v", candidates)
	}
}

func TestCollectionWaitsForDebounceRestore(t *testing.T) {
	loadErr := errors.New("database unavailable")
	queue := &debounceQueue{loadErr: loadErr}
	now := time.Unix(1730000000, 0).UTC()
	source := &mutableSource{readings: []domain.Reading{{
		DeviceID: "inv-01", AssetType: domain.AssetSolarInverter, TakenAt: now, InverterFailed: true,
		Payload: testPayload("inv-01", "site-01", now),
	}}}
	c, err := collector.New(collector.Config{
		SiteID: "site-01", Region: "africa-west", Interval: time.Second,
	}, queue, source)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.CollectOnce(context.Background()); !errors.Is(err, loadErr) {
		t.Fatalf("CollectOnce error = %v, want restore error", err)
	}
	if source.reads != 0 {
		t.Fatalf("source reads = %d, want none before debounce restore", source.reads)
	}

	queue.loadErr = nil
	if err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if source.reads != 1 {
		t.Fatalf("source reads = %d, want one after debounce restore", source.reads)
	}
}

func TestDebounceDoesNotAdvanceWhenPersistenceFails(t *testing.T) {
	saveErr := errors.New("database full")
	queue := &debounceQueue{saveErr: saveErr}
	now := time.Unix(1730000000, 0).UTC()
	source := &mutableSource{readings: []domain.Reading{{
		DeviceID: "inv-01", AssetType: domain.AssetSolarInverter, TakenAt: now, InverterFailed: true,
		Payload: testPayload("inv-01", "site-01", now),
	}}}
	c, err := collector.New(collector.Config{
		SiteID: "site-01", Region: "africa-west", Interval: time.Second, FallbackDebounce: 30 * time.Second,
	}, queue, source)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	c.SetClock(func() time.Time { return clock })

	if err := c.CollectOnce(context.Background()); !errors.Is(err, saveErr) {
		t.Fatalf("CollectOnce error = %v, want save error", err)
	}
	queue.saveErr = nil
	clock = clock.Add(30 * time.Second)
	if err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queue.opened != 0 {
		t.Fatalf("opened incidents = %d, debounce advanced without durable state", queue.opened)
	}

	clock = clock.Add(30 * time.Second)
	if err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queue.opened != 1 {
		t.Fatalf("opened incidents = %d, want one after durable debounce", queue.opened)
	}
}

func TestDeleteFailureKeepsDebounceStateUntilRecoveryPersists(t *testing.T) {
	deleteErr := errors.New("database busy")
	queue := &debounceQueue{deleteErr: deleteErr, deleteCode: fallback.InverterFailure}
	now := time.Unix(1730000000, 0).UTC()
	source := &mutableSource{readings: []domain.Reading{{
		DeviceID: "inv-01", AssetType: domain.AssetSolarInverter, TakenAt: now, InverterFailed: true,
		Payload: testPayload("inv-01", "site-01", now),
	}}}
	c, err := collector.New(collector.Config{
		SiteID: "site-01", Region: "africa-west", Interval: time.Second, FallbackDebounce: 30 * time.Second,
	}, queue, source)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	c.SetClock(func() time.Time { return clock })

	if err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	if err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queue.opened != 1 {
		t.Fatalf("opened incidents = %d, want one", queue.opened)
	}

	source.readings[0].InverterFailed = false
	if err := c.CollectOnce(context.Background()); !errors.Is(err, deleteErr) {
		t.Fatalf("CollectOnce error = %v, want delete error", err)
	}
	if queue.resolved != 0 {
		t.Fatalf("resolved incidents = %d, want none before debounce deletion", queue.resolved)
	}

	queue.deleteErr = nil
	source.readings[0].InverterFailed = true
	clock = clock.Add(60 * time.Second)
	if err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queue.opened != 1 {
		t.Fatalf("opened incidents = %d, stale recovery cleared in-memory state", queue.opened)
	}

	source.readings[0].InverterFailed = false
	if err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queue.resolved != 1 {
		t.Fatalf("resolved incidents = %d, want one after durable deletion", queue.resolved)
	}
}

type orderedQueue struct{ steps []string }

func (q *orderedQueue) Persist(context.Context, domain.Event) (uint64, error) {
	q.steps = append(q.steps, "persist")
	return 1, nil
}
func (q *orderedQueue) OpenFallbackIncident(context.Context, fallback.Event, time.Time) (bool, error) {
	q.steps = append(q.steps, "incident")
	return true, nil
}
func (q *orderedQueue) MarkPendingUpload(context.Context, uint64) error {
	q.steps = append(q.steps, "pending")
	return nil
}
func (*orderedQueue) ResolveFallbackIncident(context.Context, string, fallback.Code, time.Time) error {
	return nil
}
func (*orderedQueue) LoadDebounceConditions(context.Context) ([]fallback.DebounceCondition, error) {
	return nil, nil
}
func (q *orderedQueue) SaveDebounceCondition(context.Context, string, fallback.Code, time.Time) error {
	q.steps = append(q.steps, "debounce")
	return nil
}
func (*orderedQueue) DeleteDebounceCondition(context.Context, string, fallback.Code) error {
	return nil
}
func (*orderedQueue) Ready(context.Context, int, int64) ([]domain.Record, error)  { return nil, nil }
func (*orderedQueue) MarkUploaded(context.Context, uint64, time.Time) error       { return nil }
func (*orderedQueue) Ack(context.Context, uint64) error                           { return nil }
func (*orderedQueue) CompleteUpload(context.Context, uint64, time.Time) error     { return nil }
func (*orderedQueue) Abandon(context.Context, uint64, string) error               { return nil }
func (*orderedQueue) MarkFailed(context.Context, uint64, time.Time, string) error { return nil }
func (*orderedQueue) MarkRejected(context.Context, uint64, time.Time, string) error {
	return nil
}
func (*orderedQueue) Purge(context.Context, time.Duration) (int64, error) { return 0, nil }
func (*orderedQueue) Stats(context.Context) (domain.QueueStats, error) {
	return domain.QueueStats{}, nil
}

type fixedSource struct{ readings []domain.Reading }

func (s fixedSource) Read(context.Context) ([]domain.Reading, error) { return s.readings, nil }

func testPayload(deviceID, siteID string, takenAt time.Time) []byte {
	p, _ := json.Marshal(map[string]interface{}{
		"device_id": deviceID, "site_id": siteID, "timestamp_utc": takenAt.UTC().Unix(),
	})
	return p
}

type mutableSource struct {
	readings []domain.Reading
	reads    int
}

func (s *mutableSource) Read(context.Context) ([]domain.Reading, error) {
	s.reads++
	return s.readings, nil
}

type debounceQueue struct {
	orderedQueue
	loadErr    error
	saveErr    error
	deleteErr  error
	deleteCode fallback.Code
	opened     int
	resolved   int
}

func (q *debounceQueue) LoadDebounceConditions(context.Context) ([]fallback.DebounceCondition, error) {
	return nil, q.loadErr
}

func (q *debounceQueue) SaveDebounceCondition(context.Context, string, fallback.Code, time.Time) error {
	return q.saveErr
}

func (q *debounceQueue) DeleteDebounceCondition(_ context.Context, _ string, code fallback.Code) error {
	if code == q.deleteCode {
		return q.deleteErr
	}
	return nil
}

func (q *debounceQueue) OpenFallbackIncident(context.Context, fallback.Event, time.Time) (bool, error) {
	q.opened++
	return true, nil
}

func (q *debounceQueue) ResolveFallbackIncident(_ context.Context, _ string, code fallback.Code, _ time.Time) error {
	if code == fallback.InverterFailure {
		q.resolved++
	}
	return nil
}

func TestUnknownBatteryTemperatureDoesNotResolveOverheat(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, queuesqlite.Config{Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "site-01", GatewayID: "gateway-01", MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	source := &mutableSource{readings: []domain.Reading{{DeviceID: "battery-01", AssetType: domain.AssetBatteryBMS, TakenAt: now, InternalTemperature: 68.2, Payload: testPayload("battery-01", "site-01", now)}}}
	c, err := collector.New(collector.Config{SiteID: "site-01", Region: "africa-west", Interval: time.Second, BatteryOverheatC: 55}, store, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	source.readings[0].InternalTemperature = 0
	source.readings[0].InternalTemperatureMissing = true
	if err := c.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.FallbackCandidates(ctx, now)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("overheat resolved by missing measurement: %+v err=%v", candidates, err)
	}
}
