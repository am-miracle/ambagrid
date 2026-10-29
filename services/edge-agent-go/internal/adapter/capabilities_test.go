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
  sts_token: true
  idempotent_refs: true
  outcome_query: false
`))
	if err != nil {
		t.Fatal(err)
	}
	want := adapter.CapabilitySet{Telemetry: true, RelayControl: true, PrepaidCredit: true, STSToken: true, IdempotentRefs: true}
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
