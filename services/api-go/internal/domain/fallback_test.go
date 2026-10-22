package domain

import "testing"

func TestCriticalCodeMatchesAssetScope(t *testing.T) {
	tests := []struct {
		code, assetType string
		want            bool
	}{
		{"BATTERY_OVERHEAT", "battery_bms", true},
		{"BATTERY_OVERHEAT", "smart_meter", false},
		{"INVERTER_FAILURE", "solar_inverter", true},
		{"INVERTER_FAILURE", "battery_bms", false},
		{"TAMPER_DETECTED", "smart_meter", true},
	}
	for _, test := range tests {
		code, err := ParseCriticalCode(test.code)
		if err != nil {
			t.Fatal(err)
		}
		if got := code.MatchesAsset(test.assetType); got != test.want {
			t.Fatalf("CriticalCode(%q).MatchesAsset(%q) = %v", test.code, test.assetType, got)
		}
	}
}

func TestParseCriticalCodeRejectsUnknownCode(t *testing.T) {
	if _, err := ParseCriticalCode("LOW_BATTERY"); err == nil {
		t.Fatal("ParseCriticalCode accepted a code outside the policy allowlist")
	}
}
