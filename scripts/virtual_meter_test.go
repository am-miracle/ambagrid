package main

import (
	"math/rand"
	"os"
	"path/filepath"
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

func TestNewDeviceStateIsUniquePerMeter(t *testing.T) {
	a, err := newDeviceState("africa-west", "ng-kaji", 1, deviceKindSmartMeter, 1)
	if err != nil {
		t.Fatalf("newDeviceState returned error: %v", err)
	}
	b, err := newDeviceState("africa-west", "ng-kaji", 1, deviceKindSmartMeter, 2)
	if err != nil {
		t.Fatalf("newDeviceState returned error: %v", err)
	}

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

func TestNewDeviceStateMatchesDemoFleetSiteNames(t *testing.T) {
	state, err := newDeviceState("africa-west", demoSitePrefix, 20, deviceKindSolarInverter, 1)
	if err != nil {
		t.Fatalf("newDeviceState returned error: %v", err)
	}

	wantTopic := "africa-west/yobe-geidam/solar_inverter/inv-2001/telemetry"
	if state.topic != wantTopic {
		t.Fatalf("topic = %q, want %q", state.topic, wantTopic)
	}
}

func TestNewDeviceStateRejectsUnknownKind(t *testing.T) {
	state, err := newDeviceState("africa-west", "ng-kaji", 1, deviceKind("weather_station"), 1)
	if err == nil {
		t.Fatal("newDeviceState returned nil error for unknown kind")
	}
	if state != nil {
		t.Fatalf("newDeviceState state = %#v, want nil", state)
	}
}

func TestNextPayloadAccruesKwhScaledByInterval(t *testing.T) {
	state := &deviceState{
		siteID:      "ng-kaji-01",
		deviceID:    "met-0101",
		deviceKind:  deviceKindSmartMeter,
		deviceType:  "DEVICE_TYPE_SMART_METER",
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
	state := &deviceState{
		siteID:      "ng-kaji-01",
		deviceID:    "met-0101",
		deviceKind:  deviceKindSmartMeter,
		deviceType:  "DEVICE_TYPE_SMART_METER",
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

func TestNextPayloadUsesDeviceTypeSpecificFields(t *testing.T) {
	battery, err := newDeviceState("africa-west", "ng-kaji", 1, deviceKindBatteryBms, 1)
	if err != nil {
		t.Fatalf("newDeviceState returned error: %v", err)
	}
	inverter, err := newDeviceState("africa-west", "ng-kaji", 1, deviceKindSolarInverter, 1)
	if err != nil {
		t.Fatalf("newDeviceState returned error: %v", err)
	}

	batteryPayload := nextPayload(battery, time.Second)
	inverterPayload := nextPayload(inverter, time.Second)

	if batteryPayload.DeviceType != "DEVICE_TYPE_BATTERY_BMS" {
		t.Fatalf("battery DeviceType = %q", batteryPayload.DeviceType)
	}
	if batteryPayload.BatterySocPct == 0 {
		t.Fatal("battery payload should report state of charge")
	}
	if inverterPayload.DeviceType != "DEVICE_TYPE_SOLAR_INVERTER" {
		t.Fatalf("inverter DeviceType = %q", inverterPayload.DeviceType)
	}
	if inverterPayload.DeviceID != "inv-0101" {
		t.Fatalf("inverter DeviceID = %q, want inv-0101", inverterPayload.DeviceID)
	}
	if batteryPayload.Metrics != nil || inverterPayload.Metrics != nil {
		t.Fatal("site kit devices must omit electrical metrics rather than report zeros")
	}
	if batteryPayload.SolarIrradiance != 0 || inverterPayload.BatterySocPct != 0 {
		t.Fatal("site kit devices must not report the other device's fields")
	}
}

func TestSolarIrradianceIsZeroAtNightAndPeaksAtNoon(t *testing.T) {
	if got := solarIrradiance(3); got != 0 {
		t.Fatalf("night irradiance = %v, want 0", got)
	}
	if got := solarIrradiance(12); got < 800 {
		t.Fatalf("noon irradiance = %v, want at least 800", got)
	}
}

func TestTLSConfigRejectsBadCAFile(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := tlsConfig(filepath.Join(dir, "missing.pem")); err == nil {
		t.Fatal("tlsConfig returned nil error for a missing CA file")
	}
	if _, err := tlsConfig(bad); err == nil {
		t.Fatal("tlsConfig returned nil error for a file without certificates")
	}
	if cfg, err := tlsConfig(""); err != nil || cfg.RootCAs != nil {
		t.Fatalf("tlsConfig(\"\") = %v, %v; want system roots and no error", cfg, err)
	}
}

func TestEnvHelpersReturnFallbacks(t *testing.T) {
	t.Setenv("SIMULATOR_TEST_INT", "")
	t.Setenv("SIMULATOR_TEST_BOOL", "")
	t.Setenv("SIMULATOR_TEST_DURATION", "")

	gotInt, err := envInt("SIMULATOR_TEST_INT", 7)
	if err != nil {
		t.Fatalf("envInt returned error: %v", err)
	}
	if gotInt != 7 {
		t.Fatalf("envInt fallback = %d, want 7", gotInt)
	}

	gotBool, err := envBool("SIMULATOR_TEST_BOOL", true)
	if err != nil {
		t.Fatalf("envBool returned error: %v", err)
	}
	if !gotBool {
		t.Fatal("envBool fallback = false, want true")
	}

	gotDuration, err := envDuration("SIMULATOR_TEST_DURATION", 3*time.Second)
	if err != nil {
		t.Fatalf("envDuration returned error: %v", err)
	}
	if gotDuration != 3*time.Second {
		t.Fatalf("envDuration fallback = %s, want 3s", gotDuration)
	}
}

func TestEnvHelpersReturnParseErrors(t *testing.T) {
	t.Setenv("SIMULATOR_TEST_INT", "many")
	t.Setenv("SIMULATOR_TEST_BOOL", "sometimes")
	t.Setenv("SIMULATOR_TEST_DURATION", "later")

	if _, err := envInt("SIMULATOR_TEST_INT", 7); err == nil {
		t.Fatal("envInt returned nil error for invalid integer")
	}
	if _, err := envBool("SIMULATOR_TEST_BOOL", true); err == nil {
		t.Fatal("envBool returned nil error for invalid boolean")
	}
	if _, err := envDuration("SIMULATOR_TEST_DURATION", 3*time.Second); err == nil {
		t.Fatal("envDuration returned nil error for invalid duration")
	}
}
