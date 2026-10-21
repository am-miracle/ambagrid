package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCriticalCodesMatchSharedContract(t *testing.T) {
	var contract struct {
		Codes          []string `json:"codes"`
		EventKeyPrefix string   `json:"event_key_prefix"`
		Rules          []struct {
			Code                string `json:"code"`
			AssetType           string `json:"asset_type"`
			AlertKind           string `json:"alert_kind"`
			RequiresTemperature bool   `json:"requires_temperature"`
			SiteScoped          bool   `json:"site_scoped"`
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
	got := []string{string(BatteryOverheatCode), string(InverterFailureCode), string(TamperDetectedCode), string(SiteOutageCode)}
	if !reflect.DeepEqual(got, contract.Codes) {
		t.Fatalf("critical codes = %v, shared contract = %v", got, contract.Codes)
	}
	if got, want := string(NewEventKey("site-01", 7)), contract.EventKeyPrefix+"site-01:7"; got != want {
		t.Fatalf("event key = %q, shared contract = %q", got, want)
	}
	for _, rule := range contract.Rules {
		code, err := ParseCriticalCode(rule.Code)
		if err != nil {
			t.Fatal(err)
		}
		assetID := "asset-01"
		if rule.SiteScoped {
			assetID = "site"
		} else {
			assetType := rule.AssetType
			if assetType == "*" {
				assetType = "smart_meter"
			}
			if !code.MatchesAsset(assetType) {
				t.Errorf("code %s does not match contract asset type %s", code, rule.AssetType)
			}
		}
		event := CriticalFallbackEvent{Code: code, AssetID: assetID}
		if rule.RequiresTemperature {
			temperature := 68.2
			event.TemperatureC = &temperature
		}
		kind, _, err := event.AlertDetails()
		if err != nil {
			t.Fatalf("alert details for %s: %v", code, err)
		}
		if kind != rule.AlertKind {
			t.Errorf("alert kind for %s = %q, contract = %q", code, kind, rule.AlertKind)
		}
	}
}
