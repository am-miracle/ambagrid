package postgres

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
		if got := criticalCodeMatchesAsset(test.code, test.assetType); got != test.want {
			t.Fatalf("criticalCodeMatchesAsset(%q, %q) = %v", test.code, test.assetType, got)
		}
	}
}
