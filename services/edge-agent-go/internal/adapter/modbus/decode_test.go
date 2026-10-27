package modbus_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/modbus"
	"edge-agent-go/internal/domain"
)

var decodeAt = time.Date(2026, 10, 25, 12, 0, 0, 0, time.FixedZone("WAT", 3600))

var battery = modbus.Device{SiteID: "site-01", DeviceID: "battery-01"}

func victron(t *testing.T) modbus.Map {
	t.Helper()
	m, err := modbus.LoadMap(victronMap)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func near(got *float64, want float64) bool {
	return got != nil && math.Abs(*got-want) < 1e-9
}

func TestDecodeVictronBatteryRegisters(t *testing.T) {
	r, err := modbus.Decode(victron(t), modbus.Registers{modbus.Holding: {259: 5234, 261: 65413, 262: 315, 266: 725}}, battery, decodeAt)
	if err != nil {
		t.Fatal(err)
	}
	if r.DeviceID != "battery-01" || r.AssetType != domain.AssetBatteryBMS {
		t.Fatalf("identity = %q %q", r.DeviceID, r.AssetType)
	}
	if !r.TakenAt.Equal(decodeAt) || r.TakenAt.Location() != time.UTC || !r.LastSeenAt.Equal(decodeAt) {
		t.Fatalf("times = %s / %s, want %s in UTC", r.TakenAt, r.LastSeenAt, decodeAt)
	}
	if !near(r.Metrics.Voltage, 52.34) || !near(r.Metrics.Current, -12.3) {
		t.Fatalf("metrics = %v V %v A", *r.Metrics.Voltage, *r.Metrics.Current)
	}
	if !near(r.InternalTemperature, 31.5) || !near(r.BatterySOCPct, 72.5) {
		t.Fatalf("battery = %v C, %v %%", r.InternalTemperature, r.BatterySOCPct)
	}
	var payload map[string]any
	if err := json.Unmarshal(r.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["site_id"] != "site-01" || payload["device_id"] != "battery-01" || payload["device_type"] != "DEVICE_TYPE_BATTERY_BMS" ||
		payload["timestamp_utc"] != float64(decodeAt.Unix()) || payload["battery_soc_pct"] != 72.5 || payload["internal_temperature"] != 31.5 {
		t.Fatalf("payload = %v", payload)
	}
}

func TestDecodeThirtyTwoBitTypesInBothWordOrders(t *testing.T) {
	m, err := modbus.ParseMap([]byte(`
device_type: smart_meter
manufacturer: test
model: meter
points:
  - {field: current, address: 10, function: input, type: i32, word_order: big, scale: 1, unit: A}
  - {field: voltage, address: 20, function: input, type: i32, word_order: little, scale: 1, unit: V}
  - {field: total_kwh, address: 30, function: holding, type: u32, word_order: big, scale: 0.001, unit: kWh}
  - {field: frequency, address: 40, function: holding, type: f32, word_order: big, scale: 1, unit: Hz}
`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := modbus.Decode(m, modbus.Registers{
		modbus.Input:   {10: 0xffff, 11: 0xfffe, 20: 0xfffe, 21: 0xffff},
		modbus.Holding: {30: 0x0001, 31: 0x86a0, 40: 0x4248, 41: 0x0000},
	}, modbus.Device{SiteID: "s", DeviceID: "meter-01"}, decodeAt)
	if err != nil {
		t.Fatal(err)
	}
	if !near(r.Metrics.Current, -2) || !near(r.Metrics.Voltage, -2) || !near(r.Metrics.TotalKWh, 100) || !near(r.Metrics.Frequency, 50) {
		t.Fatalf("metrics = %+v", r.Metrics)
	}
}

func TestDecodeKeepsUnavailableOptionalPointsMissing(t *testing.T) {
	r, err := modbus.Decode(victron(t), modbus.Registers{modbus.Holding: {259: 5234, 261: 32767, 266: 65535}}, battery, decodeAt)
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.Current != nil || r.InternalTemperature != nil || r.BatterySOCPct != nil {
		t.Fatalf("missing values manufactured: %+v", r)
	}
	if strings.Contains(string(r.Payload), "internal_temperature") || strings.Contains(string(r.Payload), "battery_soc_pct") {
		t.Fatalf("payload carries unknown values: %s", r.Payload)
	}
}

func TestDecodeRejectsUnusableRequiredValues(t *testing.T) {
	for name, regs := range map[string]modbus.Registers{
		"required sentinel": {modbus.Holding: {259: 65535}},
		"required absent":   {modbus.Holding: {266: 725}},
		"SoC over 100":      {modbus.Holding: {259: 5234, 266: 1001}},
	} {
		if _, err := modbus.Decode(victron(t), regs, battery, decodeAt); !errors.Is(err, adapter.ErrInvalidResponse) {
			t.Errorf("%s: error = %v, want ErrInvalidResponse", name, err)
		}
	}
}
