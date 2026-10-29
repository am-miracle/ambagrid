package simulator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/adapter/adaptertest"
	"edge-agent-go/internal/adapter/simulator"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

var fixtures = os.DirFS("../testdata/simulator")

func TestTelemetryConformance(t *testing.T) {
	adaptertest.RunTelemetry(t, func(t *testing.T) adaptertest.TelemetryHarness {
		a := simulator.New("site-01")
		return adaptertest.TelemetryHarness{Adapter: a, Device: a.Device(), Fixtures: fixtures}
	})
}

func TestCommandConformance(t *testing.T) {
	for name, config := range map[string]struct {
		options     []simulator.Option
		fixturePath string
	}{
		"Default": {fixturePath: "../testdata/simulator"},
		"TelemetryOnly": {
			options:     []simulator.Option{simulator.WithCapabilities(adapter.CapabilitySet{Telemetry: true})},
			fixturePath: "../testdata/simulator/status",
		},
	} {
		t.Run(name, func(t *testing.T) {
			adaptertest.RunCommand(t, func(t *testing.T) adaptertest.CommandHarness {
				a := simulator.New("site-01", config.options...)
				return adaptertest.CommandHarness{
					Adapter:  adapter.NewLedger(ledgerStore(t), a),
					Device:   a.Device(),
					Fixtures: os.DirFS(config.fixturePath),
				}
			})
		})
	}
}

func ledgerStore(t *testing.T) *queuesqlite.Store {
	t.Helper()
	store, err := queuesqlite.Open(context.Background(), queuesqlite.Config{
		Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "site-01", GatewayID: "gateway-01",
		MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
