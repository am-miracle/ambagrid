package main

import (
	"context"
	"encoding/json"
	"testing"

	"edge-agent-go/internal/adapter/simulator"
	"edge-agent-go/internal/domain"
)

func TestFleetReadingsHaveGlobalIdentitiesAndMatchingPayloads(t *testing.T) {
	seen := map[string]bool{}
	for _, site := range []string{"rivers-bolo", "bayelsa-oweikorogha"} {
		source := &fleetSource{hardware: simulator.New(site), siteID: site, meters: 10}
		readings, err := source.Read(context.Background())
		if err != nil || len(readings) != 12 {
			t.Fatalf("site=%s count=%d err=%v", site, len(readings), err)
		}
		counts := map[domain.AssetType]int{}
		for _, reading := range readings {
			if seen[reading.DeviceID] {
				t.Fatalf("asset ID reused across the fleet: %s", reading.DeviceID)
			}
			seen[reading.DeviceID] = true
			counts[reading.AssetType]++
			var payload struct {
				DeviceID string                    `json:"device_id"`
				SiteID   string                    `json:"site_id"`
				Metrics  *domain.ElectricalMetrics `json:"metrics"`
			}
			if err := json.Unmarshal(reading.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.DeviceID != reading.DeviceID || payload.SiteID != site {
				t.Fatalf("reading and wire identity differ: %+v payload=%+v", reading, payload)
			}
			if reading.Metrics != nil && (payload.Metrics == nil || *payload.Metrics.ActivePower != *reading.Metrics.ActivePower) {
				t.Fatal("per-meter values differ between the reading and wire payload")
			}
		}
		if counts[domain.AssetSmartMeter] != 10 || counts[domain.AssetBatteryBMS] != 1 || counts[domain.AssetSolarInverter] != 1 {
			t.Fatalf("unexpected site equipment: %+v", counts)
		}
	}
}
