package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v2"
)

// DeviceConfig selects independent telemetry and command adapters for one
// platform device. MQTT transport credentials remain in environment settings.
type DeviceConfig struct {
	ID               string              `yaml:"id"`
	Telemetry        string              `yaml:"telemetry"`
	Command          string              `yaml:"command"`
	CapabilitiesFile string              `yaml:"capabilities_file"`
	Modbus           *DeviceModbusConfig `yaml:"modbus"`
}

type DeviceModbusConfig struct {
	Address string        `yaml:"address"`
	UnitID  *uint8        `yaml:"unit_id"`
	MapFile string        `yaml:"map_file"`
	Timeout time.Duration `yaml:"timeout"`
}

func (d DeviceConfig) ModbusSettings() ModbusConfig {
	return ModbusConfig{Address: d.Modbus.Address, UnitID: *d.Modbus.UnitID, DeviceID: d.ID, MapFile: d.Modbus.MapFile, Timeout: d.Modbus.Timeout}
}

func loadDevices(path string) ([]DeviceConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read device inventory: %w", err)
	}
	var inventory struct {
		Devices []DeviceConfig `yaml:"devices"`
	}
	if err := yaml.UnmarshalStrict(raw, &inventory); err != nil {
		return nil, fmt.Errorf("parse device inventory: %w", err)
	}
	if len(inventory.Devices) == 0 {
		return nil, fmt.Errorf("device inventory must contain at least one device")
	}
	seen := map[string]bool{}
	resolve := func(value string) string {
		if value == "" || filepath.IsAbs(value) {
			return value
		}
		return filepath.Join(filepath.Dir(path), value)
	}
	for i := range inventory.Devices {
		d := &inventory.Devices[i]
		if strings.TrimSpace(d.ID) == "" || strings.ContainsAny(d.ID, "/+#\x00{}") || seen[d.ID] {
			return nil, fmt.Errorf("device ID %q is empty, duplicated or unsafe", d.ID)
		}
		seen[d.ID] = true
		if d.Telemetry == "" && d.Command == "" {
			return nil, fmt.Errorf("device %q needs a telemetry or command adapter", d.ID)
		}
		switch d.Telemetry {
		case "":
		case "simulator":
			if d.ID != "meter-01" && d.ID != "battery-01" && d.ID != "inverter-01" {
				return nil, fmt.Errorf("device %q: simulator telemetry IDs are meter-01, battery-01 and inverter-01", d.ID)
			}
		case "modbus":
			if d.Modbus == nil || d.Modbus.UnitID == nil || d.Modbus.MapFile == "" {
				return nil, fmt.Errorf("device %q: modbus requires address, unit_id and map_file", d.ID)
			}
			if _, _, err := net.SplitHostPort(d.Modbus.Address); err != nil {
				return nil, fmt.Errorf("device %q Modbus address: %w", d.ID, err)
			}
			if d.Modbus.Timeout < 0 {
				return nil, fmt.Errorf("device %q Modbus timeout must be positive", d.ID)
			}
			if d.Modbus.Timeout == 0 {
				d.Modbus.Timeout = 5 * time.Second
			}
			d.Modbus.MapFile = resolve(d.Modbus.MapFile)
		default:
			return nil, fmt.Errorf("device %q: unknown telemetry adapter %q", d.ID, d.Telemetry)
		}
		if d.Telemetry != "modbus" && d.Modbus != nil {
			return nil, fmt.Errorf("device %q has modbus settings without a modbus adapter", d.ID)
		}
		switch d.Command {
		case "":
		case "simulator":
			if d.ID != "meter-01" {
				return nil, fmt.Errorf("device %q: simulator commands require meter-01", d.ID)
			}
		case "mqtt":
			if d.CapabilitiesFile == "" {
				return nil, fmt.Errorf("device %q MQTT requires capabilities_file", d.ID)
			}
			d.CapabilitiesFile = resolve(d.CapabilitiesFile)
		default:
			return nil, fmt.Errorf("device %q: unknown command adapter %q", d.ID, d.Command)
		}
		if d.Command != "mqtt" && d.CapabilitiesFile != "" {
			return nil, fmt.Errorf("device %q has capabilities_file without MQTT commands", d.ID)
		}
	}
	return inventory.Devices, nil
}
