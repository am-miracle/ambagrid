package config

import (
	"fmt"
	"strings"
)

// normalizeDevices gives environment-only development the production device
// inventory. No site-wide command fallback survives: MQTT must name its device.
func (cfg *Config) normalizeDevices(selection, mqttDeviceID string) error {
	if cfg.Collector.Enabled {
		for _, id := range []string{"meter-01", "battery-01", "inverter-01"} {
			cfg.Devices = append(cfg.Devices, DeviceConfig{ID: id, Telemetry: Simulator})
		}
	}
	if cfg.Modbus.Address != "" {
		unit := cfg.Modbus.UnitID
		cfg.Devices = append(cfg.Devices, DeviceConfig{ID: cfg.Modbus.DeviceID, Telemetry: Modbus, Modbus: &DeviceModbusConfig{Address: cfg.Modbus.Address, UnitID: &unit, MapFile: cfg.Modbus.MapFile, Timeout: cfg.Modbus.Timeout}})
	}
	if !cfg.Commands.Enabled {
		return nil
	}
	kind, err := parseAdapterKind(selection)
	if err != nil {
		return err
	}
	if kind == Simulator {
		if !cfg.Collector.Enabled {
			return fmt.Errorf("EDGE_COMMAND_ADAPTER=simulator requires EDGE_SIMULATOR_ENABLED")
		}
		cfg.Devices[0].Command = Simulator
		return nil
	}
	if kind != MQTT {
		return fmt.Errorf("adapter %q does not support commands", kind)
	}
	if strings.TrimSpace(mqttDeviceID) == "" || strings.ContainsAny(mqttDeviceID, "/+#\x00{}") {
		return fmt.Errorf("MQTT commands require EDGE_MQTT_DEVICE_ID or a device inventory; site-wide command routing is not supported")
	}
	if cfg.Commands.MQTT.CapabilitiesFile == "" {
		return fmt.Errorf("MQTT commands require EDGE_MQTT_CAPABILITIES_FILE")
	}
	for i := range cfg.Devices {
		if cfg.Devices[i].ID == mqttDeviceID {
			cfg.Devices[i].Command = MQTT
			cfg.Devices[i].CapabilitiesFile = cfg.Commands.MQTT.CapabilitiesFile
			return nil
		}
	}
	cfg.Devices = append(cfg.Devices, DeviceConfig{ID: mqttDeviceID, Command: MQTT, CapabilitiesFile: cfg.Commands.MQTT.CapabilitiesFile})
	return nil
}
