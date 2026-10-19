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
}
