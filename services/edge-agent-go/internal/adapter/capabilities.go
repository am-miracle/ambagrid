package adapter

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v2"
)

// LoadCapabilities reads a device capability declaration in the YAML form of
// docs/hardware-adapters.md. Unknown keys are errors, so a misspelled
// capability is caught instead of silently read as false.
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
	if caps.Capabilities.STSToken && !caps.Capabilities.PrepaidCredit {
		return Capabilities{}, fmt.Errorf("capability declaration has sts_token without prepaid_credit")
	}
	return caps, nil
}
