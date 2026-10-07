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
