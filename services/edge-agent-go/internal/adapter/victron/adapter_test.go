package victron_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"edge-agent-go/internal/adapter/adaptertest"
	"edge-agent-go/internal/adapter/devicetest"
	"edge-agent-go/internal/adapter/victron"
)

type device struct {
	gate devicetest.Gate
	raw  []byte
}

func (d *device) Inject(raw []byte)      { d.raw = raw }
func (d *device) Stall() <-chan struct{} { return d.gate.Stall() }
func (d *device) ReadHolding(ctx context.Context, unit byte, address uint16) (uint16, error) {
	if err := d.gate.Enter(ctx); err != nil {
		return 0, err
	}
	var registers map[uint16]uint16
	if err := json.Unmarshal(d.raw, &registers); err != nil {
		return 0, err
	}
	value, ok := registers[address]
	if !ok {
		return 0, victron.ErrRegisterUnavailable
	}
	return value, nil
}
func TestTelemetryConformance(t *testing.T) {
	for _, path := range []string{"../testdata/victron", "../testdata/victron/voltage-only"} {
		t.Run(path, func(t *testing.T) {
			adaptertest.RunTelemetry(t, func(t *testing.T) adaptertest.TelemetryHarness {
				raw, err := os.ReadFile(path + "/raw_input.json")
				if err != nil {
					t.Fatal(err)
				}
				d := &device{raw: raw}
				a, err := victron.New(victron.Config{SiteID: "site-01", DeviceID: "battery-01", UnitID: 225, Now: func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }}, d)
				if err != nil {
					t.Fatal(err)
				}
				return adaptertest.TelemetryHarness{Adapter: a, Device: d, Fixtures: os.DirFS(path)}
			})
		})
	}
}

func TestOptionalRegistersRemainUnknown(t *testing.T) {
	d := &device{raw: []byte(`{"259":5234,"261":32767}`)}
	a, err := victron.New(victron.Config{SiteID: "site-01", DeviceID: "battery-01", UnitID: 225}, d)
	if err != nil {
		t.Fatal(err)
	}
	readings, err := a.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := readings[0]
	var payload map[string]any
	if err := json.Unmarshal(r.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if r.Metrics.Current != nil || !r.InternalTemperatureMissing || !r.BatterySOCMissing {
		t.Fatalf("missing values manufactured: %+v", r)
	}
	if _, ok := payload["internal_temperature"]; ok {
		t.Fatal("unknown temperature serialized")
	}
	if _, ok := payload["battery_soc_pct"]; ok {
		t.Fatal("unknown SoC serialized")
	}
}
