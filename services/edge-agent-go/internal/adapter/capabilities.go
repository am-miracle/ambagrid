package adapter

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v2"
)

// LoadCapabilities rejects unknown keys, so a misspelled capability is caught
// instead of silently read as false.
func LoadCapabilities(path string) (Capabilities, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Capabilities{}, fmt.Errorf("read capability declaration: %w", err)
	}
	return ParseCapabilities(data)
}

func ParseCapabilities(data []byte) (Capabilities, error) {
	var caps Capabilities
	if err := yaml.UnmarshalStrict(bytes.TrimSpace(data), &caps); err != nil {
		return Capabilities{}, fmt.Errorf("parse capability declaration: %w", err)
	}
	if !caps.DeviceType.Valid() {
		return Capabilities{}, fmt.Errorf("capability declaration device_type %q is not a known asset type", caps.DeviceType)
	}
	if strings.TrimSpace(caps.Manufacturer) == "" || strings.TrimSpace(caps.Model) == "" {
		return Capabilities{}, fmt.Errorf("capability declaration needs a manufacturer and model")
	}
	if len(caps.FirmwareVersions) > 0 && !caps.Capabilities.Telemetry {
		return Capabilities{}, fmt.Errorf("firmware verification requires status capability")
	}
	for _, v := range caps.FirmwareVersions {
		if strings.TrimSpace(v) == "" || len(v) > 200 {
			return Capabilities{}, fmt.Errorf("invalid firmware version")
		}
	}
	for code, fault := range caps.FaultCodes {
		if strings.TrimSpace(code) == "" || strings.TrimSpace(fault.Description) == "" || len(fault.Description) > 500 {
			return Capabilities{}, fmt.Errorf("fault codes require a code and description")
		}
		switch fault.Severity {
		case "info", "warning", "critical":
		default:
			return Capabilities{}, fmt.Errorf("invalid fault severity for %q", code)
		}
	}
	for commandType, template := range caps.CommandTemplates {
		sample := Command{ID: "sample", Type: commandType, DeviceID: "sample", CreditKWh: 1}
		if !slices.Contains(CommandTypes, commandType) {
			return Capabilities{}, fmt.Errorf("command template for unknown command %q", commandType)
		}
		if err := caps.Supports(sample); err != nil {
			return Capabilities{}, fmt.Errorf("command template: %w", err)
		}
		if _, err := RenderCommandTemplate(template, sample); err != nil {
			return Capabilities{}, fmt.Errorf("command template: %w", err)
		}
	}
	return caps, nil
}
