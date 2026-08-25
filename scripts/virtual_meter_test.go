package main

import (
	"math/rand"
	"testing"
	"time"
)

func TestRandomFloatWithinRange(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		got := randomFloat(rng, 10, 20)
		if got < 10 || got >= 20 {
			t.Fatalf("randomFloat(10, 20) = %v, want value in [10, 20)", got)
		}
	}
}

func TestNewMeterStateIsUniquePerMeter(t *testing.T) {
	a := newMeterState("africa-west", "ng-kaji", 1, 1)
	b := newMeterState("africa-west", "ng-kaji", 1, 2)

	if a.deviceID == b.deviceID {
		t.Fatalf("expected distinct device IDs, got %q for both", a.deviceID)
	}
	if a.topic == b.topic {
		t.Fatalf("expected distinct topics, got %q for both", a.topic)
	}

	wantTopic := "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry"
	if a.topic != wantTopic {
		t.Fatalf("topic = %q, want %q", a.topic, wantTopic)
	}
	wantHousehold := "house-0101"
	if a.householdID != wantHousehold {
		t.Fatalf("householdID = %q, want %q", a.householdID, wantHousehold)
	}
}

func TestNextPayloadAccruesKwhScaledByInterval(t *testing.T) {
	state := &meterState{
		siteID:      "ng-kaji-01",
		deviceID:    "met-0101",
		householdID: "house-0101",
		baseKwh:     1000,
		rng:         rand.New(rand.NewSource(1)),
	}

	payload := nextPayload(state, 5*time.Second)

	wantDelta := float64(payload.Metrics.ActivePower) * (5 * time.Second).Hours()
	gotDelta := payload.Metrics.TotalKwh - 1000
	if diff := gotDelta - wantDelta; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("total_kwh delta = %v, want %v", gotDelta, wantDelta)
	}
	if state.baseKwh != payload.Metrics.TotalKwh {
		t.Fatalf("state.baseKwh = %v, want it mutated to match payload TotalKwh %v", state.baseKwh, payload.Metrics.TotalKwh)
	}
}

func TestNextPayloadUsesStateIdentity(t *testing.T) {
	state := &meterState{
		siteID:      "ng-kaji-01",
		deviceID:    "met-0101",
		householdID: "house-0101",
		baseKwh:     1000,
		rng:         rand.New(rand.NewSource(1)),
	}

	payload := nextPayload(state, time.Second)

	if payload.SiteID != "ng-kaji-01" {
		t.Errorf("SiteID = %q, want %q", payload.SiteID, "ng-kaji-01")
	}
	if payload.DeviceID != "met-0101" {
		t.Errorf("DeviceID = %q, want %q", payload.DeviceID, "met-0101")
	}
	if payload.HouseholdID != "house-0101" {
		t.Errorf("HouseholdID = %q, want %q", payload.HouseholdID, "house-0101")
	}
	if payload.DeviceType != "DEVICE_TYPE_SMART_METER" {
		t.Errorf("DeviceType = %q, want %q", payload.DeviceType, "DEVICE_TYPE_SMART_METER")
	}
}
