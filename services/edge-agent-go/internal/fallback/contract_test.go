package fallback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCriticalCodesMatchSharedContract(t *testing.T) {
	var contract struct {
		Codes          []string `json:"codes"`
		EventKeyPrefix string   `json:"event_key_prefix"`
		Rules          []struct {
			Code                Code `json:"code"`
			RequiresTemperature bool `json:"requires_temperature"`
			SiteScoped          bool `json:"site_scoped"`
		} `json:"rules"`
	}
	path := filepath.Join("..", "..", "..", "..", "proto", "fixtures", "critical_fallback_v1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	got := []string{string(BatteryOverheat), string(InverterFailure), string(TamperDetected), string(SiteOutage)}
	if !reflect.DeepEqual(got, contract.Codes) {
		t.Fatalf("critical codes = %v, shared contract = %v", got, contract.Codes)
	}
	if got, want := string(NewEventKey("site-01", 7)), contract.EventKeyPrefix+"site-01:7"; got != want {
		t.Fatalf("event key = %q, shared contract = %q", got, want)
	}
	for _, rule := range contract.Rules {
		event := Event{
			SiteID: "site-01", AssetID: "asset-01", Sequence: 7,
			Code: rule.Code, OccurredAt: time.Unix(1730000000, 0).UTC(),
		}
		if rule.SiteScoped {
			event.AssetID = "site"
		}
		if rule.RequiresTemperature {
			temperature := 68.2
			event.TemperatureC = &temperature
		}
		if _, err := Format(event); err != nil {
			t.Errorf("contract rule %s does not format: %v", rule.Code, err)
		}
	}
}
