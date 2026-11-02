package adapter_test

import (
	"strings"
	"testing"

	"edge-agent-go/internal/adapter"
)

func TestParseCapabilitiesReadsTheDocumentedDeclaration(t *testing.T) {
	caps, err := adapter.ParseCapabilities([]byte(`
device_type: smart_meter
manufacturer: hexing
model: example
capabilities:
  telemetry: true
  relay_control: true
  prepaid_credit: true
  idempotent_refs: true
  outcome_query: false
`))
	if err != nil {
		t.Fatal(err)
	}
	want := adapter.CapabilitySet{Telemetry: true, RelayControl: true, PrepaidCredit: true, IdempotentRefs: true}
	if caps.DeviceType != "smart_meter" || caps.Manufacturer != "hexing" || caps.Model != "example" || caps.Capabilities != want {
		t.Fatalf("capabilities = %+v", caps)
	}
}

func TestParseCapabilitiesRejectsMistakes(t *testing.T) {
	for name, c := range map[string]struct{ yaml, want string }{
		"misspelled capability": {"device_type: smart_meter\nmanufacturer: m\nmodel: x\ncapabilities:\n  relay_contol: true\n", "relay_contol"},
		"unknown device type":   {"device_type: toaster\nmanufacturer: m\nmodel: x\n", "toaster"},
		"missing model":         {"device_type: smart_meter\nmanufacturer: m\n", "model"},
		"token without credit":  {"device_type: smart_meter\nmanufacturer: m\nmodel: x\ncapabilities:\n  sts_token: true\n", "sts_token"},
	} {
		if _, err := adapter.ParseCapabilities([]byte(c.yaml)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want one mentioning %q", name, err, c.want)
		}
	}
}

func TestCommandTemplatesAreValidatedAtLoad(t *testing.T) {
	const base = "device_type: smart_meter\nmanufacturer: m\nmodel: x\ncapabilities:\n  telemetry: true\n  relay_control: true\n"
	for name, c := range map[string]struct{ yaml, want string }{
		"unknown command":     {"command_templates:\n  reboot: '{\"cmd\":\"reboot\"}'\n", "unknown command"},
		"unsupported command": {"command_templates:\n  credit_meter: '{\"wh\":{wh}}'\n", "does not support"},
		"unknown placeholder": {"command_templates:\n  disconnect_meter: '{\"m\":{meter}}'\n", "unknown placeholder"},
		"credit placeholder":  {"command_templates:\n  disconnect_meter: '{\"wh\":{wh}}'\n", "unknown placeholder"},
		"invalid JSON":        {"command_templates:\n  disconnect_meter: 'open {device}'\n", "JSON object"},
		"not an object":       {"command_templates:\n  disconnect_meter: '{device}'\n", "JSON object"},
	} {
		if _, err := adapter.ParseCapabilities([]byte(base + c.yaml)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want one mentioning %q", name, err, c.want)
		}
	}
	if _, err := adapter.ParseCapabilities([]byte(base + "command_templates:\n  disconnect_meter: '{\"cmd\":\"open\",\"meter\":{device}}'\n")); err != nil {
		t.Fatal(err)
	}
}

func TestRenderCommandTemplateEscapesValues(t *testing.T) {
	got, err := adapter.RenderCommandTemplate(`{"m":{device},"wh":{wh}}`, adapter.Command{Type: adapter.CommandCredit, DeviceID: `me"ter`, CreditKWh: 1.5})
	if err != nil || string(got) != `{"m":"me\"ter","wh":1500}` {
		t.Fatalf("rendered %s, err %v", got, err)
	}
}
