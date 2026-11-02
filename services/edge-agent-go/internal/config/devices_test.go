package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edge-agent-go/internal/config"
)

func inventoryEnv(t *testing.T, data string) (config.Config, error) {
	t.Helper()
	t.Setenv("EDGE_SITE_ID", "site-01")
	t.Setenv("EDGE_GATEWAY_ID", "edge-01")
	t.Setenv("EDGE_SIMULATOR_ENABLED", "false")
	t.Setenv("EDGE_MODBUS_ADDRESS", "")
	t.Setenv("EDGE_COMMAND_ADAPTER", "")
	t.Setenv("EDGE_MQTT_CAPABILITIES_FILE", "")
	path := filepath.Join(t.TempDir(), "devices.yaml")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDGE_DEVICES_FILE", path)
	return config.FromEnv()
}

func TestInventoryChoosesAdaptersPerDevice(t *testing.T) {
	cfg, err := inventoryEnv(t, `devices:
 - id: meter-01
   telemetry: simulator
   command: simulator
 - id: meter-02
   command: mqtt
   capabilities_file: meter.yaml
 - id: battery-02
   telemetry: modbus
   modbus: {address: "127.0.0.1:502", unit_id: 225, map_file: battery.yaml, timeout: 2s}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Devices) != 3 || cfg.Devices[1].Command != "mqtt" || cfg.Devices[2].Modbus.Timeout.String() != "2s" || *cfg.Devices[2].Modbus.UnitID != 225 {
		t.Fatalf("devices=%+v", cfg.Devices)
	}
	if cfg.Devices[1].CapabilitiesFile != filepath.Join(filepath.Dir(cfg.DevicesFile), "meter.yaml") {
		t.Fatal("relative paths not resolved against inventory")
	}
}

func TestInventoryRejectsAmbiguousOrIncompleteMappings(t *testing.T) {
	for name, data := range map[string]string{
		"empty":                "devices: []",
		"duplicate":            "devices: [{id: meter-01, command: simulator}, {id: meter-01, command: mqtt, capabilities_file: a.yaml}]",
		"unknown key":          "devices: [{id: meter-01, comand: simulator}]",
		"unknown adapter":      "devices: [{id: meter-01, command: vendor}]",
		"missing map":          "devices: [{id: battery, telemetry: modbus, modbus: {address: 'localhost:502', unit_id: 1}}]",
		"missing unit":         "devices: [{id: battery, telemetry: modbus, modbus: {address: 'localhost:502', map_file: a.yaml}}]",
		"missing capabilities": "devices: [{id: meter-02, command: mqtt}]",
		"wildcard device":      "devices: [{id: 'meter/+', command: mqtt, capabilities_file: a.yaml}]",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := inventoryEnv(t, data); err == nil {
				t.Fatal("invalid inventory accepted")
			}
		})
	}
}

func TestInventoryCannotSilentlyMixWithLegacySelection(t *testing.T) {
	_, err := inventoryEnv(t, "devices: [{id: meter-01, command: simulator}]")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDGE_COMMAND_ADAPTER", "simulator")
	if _, err := config.FromEnv(); err == nil || !strings.Contains(err.Error(), "replaces") {
		t.Fatalf("mixed configuration: %v", err)
	}
}

func TestInventoryMQTTRequiresProductionIsolationWithoutGlobalCapabilities(t *testing.T) {
	t.Setenv("EDGE_COMMANDS_ENABLED", "true")
	t.Setenv("EDGE_COMMANDS_ENDPOINT", "http://127.0.0.1:8080")
	t.Setenv("EDGE_COMMANDS_API_KEY", "test-site-key")
	t.Setenv("EDGE_MQTT_ALLOW_INSECURE", "false")
	t.Setenv("EDGE_MQTT_BROKER", "mqtt://localhost:1883")
	if _, err := inventoryEnv(t, "devices: [{id: meter-02, command: mqtt, capabilities_file: meter.yaml}]"); err == nil {
		t.Fatal("insecure inventory MQTT accepted")
	}
	t.Setenv("EDGE_MQTT_BROKER", "ssl://localhost:8883")
	t.Setenv("EDGE_MQTT_USERNAME", "gateway-test")
	t.Setenv("EDGE_MQTT_PASSWORD", "fixture-password")
	t.Setenv("EDGE_MQTT_CA_FILE", "fixture-ca.pem")
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands.MQTT.CapabilitiesFile != "" || cfg.Devices[0].CapabilitiesFile == "" {
		t.Fatal("per-device capabilities not selected")
	}
}
