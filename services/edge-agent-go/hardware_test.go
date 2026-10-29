package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/config"
	"edge-agent-go/internal/health"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
	"github.com/prometheus/client_golang/prometheus"
)

func TestDeviceInventoryWiresTelemetryAndDurableCommandRoutes(t *testing.T) {
	ctx := context.Background()
	store, err := queuesqlite.Open(ctx, queuesqlite.Config{Path: filepath.Join(t.TempDir(), "queue.db"), SiteID: "site-01", GatewayID: "edge-01", MaxStorageBytes: 8 << 20, WarningPercent: 70, CriticalReservePercent: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Config{DevicesFile: "devices.yaml", Queue: config.QueueConfig{SiteID: "site-01"}, Commands: config.CommandConfig{Enabled: true}, Devices: []config.DeviceConfig{
		{ID: "meter-01", Telemetry: "simulator", Command: "simulator"},
		{ID: "battery-01", Telemetry: "simulator"},
	}}
	metrics := health.NewAdapterMetrics()
	registry := prometheus.NewRegistry()
	registry.MustRegister(metrics)
	value := func(name string) float64 {
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() == name {
				for _, metric := range family.Metric {
					for _, label := range metric.Label {
						if label.GetName() == "device_id" && label.GetValue() == "meter-01" {
							if metric.Gauge != nil {
								return metric.Gauge.GetValue()
							}
							return metric.Counter.GetValue()
						}
					}
				}
			}
		}
		t.Fatalf("missing %s for meter-01", name)
		return 0
	}
	hw, err := configureHardware(ctx, cfg, store, metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer hw.close()
	if len(hw.sources) != 2 {
		t.Fatalf("sources=%d", len(hw.sources))
	}
	for i, source := range hw.sources {
		readings, err := source.Read(ctx)
		if err != nil || len(readings) != 1 || readings[0].DeviceID != cfg.Devices[i].ID {
			t.Fatalf("source %d: %+v, %v", i, readings, err)
		}
	}
	cmd := adapter.Command{ID: "relay-1", DeviceID: "meter-01", Type: adapter.CommandDisconnect}
	if _, err := hw.executor.Execute(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	lastSuccess := value("ambagrid_edge_adapter_last_successful_command_timestamp_seconds")
	if lastSuccess == 0 {
		t.Fatal("successful hardware command did not update health")
	}
	if _, err := hw.executor.Execute(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if got := value("ambagrid_edge_adapter_last_successful_command_timestamp_seconds"); got != lastSuccess {
		t.Fatal("cached ledger result refreshed hardware health")
	}
	entry, found, err := store.LoadCommand(ctx, cmd.ID)
	if err != nil || !found || entry.State != adapter.CommandApplied || entry.Attempts != 1 {
		t.Fatalf("ledger: %+v found=%t err=%v", entry, found, err)
	}
	unsupported := adapter.Command{ID: "unsupported-token", DeviceID: "meter-01", Type: adapter.CommandCredit, STSToken: "12345678901234567890"}
	if _, err := hw.executor.Execute(ctx, unsupported); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("unsupported token: %v", err)
	}
	if value("ambagrid_edge_adapter_unsupported_total") != 1 || value("ambagrid_edge_adapter_command_errors_total") != 1 {
		t.Fatal("ledger capability refusal was not counted")
	}
	cmd.ID = "battery-command"
	cmd.DeviceID = "battery-01"
	if _, err := hw.executor.Execute(ctx, cmd); !errors.Is(err, adapter.ErrUnsupported) {
		t.Fatalf("telemetry-only command: %v", err)
	}
}
