package modbus_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/adaptertest"
	"edge-agent-go/internal/adapter/devicetest"
	"edge-agent-go/internal/adapter/modbus"
)

// device serves an injected register image, keyed by function and address.
type device struct {
	gate  devicetest.Gate
	image map[modbus.Function]map[string]uint16
	reads int
}

func (d *device) Inject(raw []byte) {
	d.image = nil
	if err := json.Unmarshal(raw, &d.image); err != nil {
		panic(err)
	}
}

func (d *device) Stall() <-chan struct{} { return d.gate.Stall() }

func (d *device) ReadRegisters(ctx context.Context, unit byte, function modbus.Function, address, quantity uint16) ([]uint16, error) {
	if err := d.gate.Enter(ctx); err != nil {
		return nil, err
	}
	d.reads++
	words := make([]uint16, quantity)
	for i := range words {
		word, ok := d.image[function][strconv.Itoa(int(address)+i)]
		if !ok {
			return nil, modbus.ErrRegisterUnavailable
		}
		words[i] = word
	}
	return words, nil
}

func newAdapter(t *testing.T, d *device) *modbus.Adapter {
	t.Helper()
	a, err := modbus.New(modbus.Config{
		Map: victron(t), Device: battery, UnitID: 225,
		Now: func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) },
	}, d)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTelemetryConformance(t *testing.T) {
	for _, path := range []string{"../testdata/modbus/victron-battery", "../testdata/modbus/victron-battery/voltage-only"} {
		t.Run(path, func(t *testing.T) {
			adaptertest.RunTelemetry(t, func(t *testing.T) adaptertest.TelemetryHarness {
				raw, err := os.ReadFile(path + "/raw_input.json")
				if err != nil {
					t.Fatal(err)
				}
				d := &device{}
				d.Inject(raw)
				return adaptertest.TelemetryHarness{Adapter: newAdapter(t, d), Device: d, Fixtures: os.DirFS(path)}
			})
		})
	}
}

type failingClient struct{ err error }

func (c failingClient) ReadRegisters(context.Context, byte, modbus.Function, uint16, uint16) ([]uint16, error) {
	return nil, c.err
}

func TestReadSurfacesTransportFailures(t *testing.T) {
	a, err := modbus.New(modbus.Config{Map: victron(t), Device: battery, UnitID: 225}, failingClient{errors.New("connection refused")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(context.Background()); err == nil || errors.Is(err, adapter.ErrInvalidResponse) {
		t.Fatalf("error = %v, want the transport failure", err)
	}
	if caps := a.Capabilities(); caps.Manufacturer != "victron" || !caps.Capabilities.Telemetry || caps.Capabilities.RelayControl {
		t.Fatalf("capabilities = %+v", caps)
	}
}
