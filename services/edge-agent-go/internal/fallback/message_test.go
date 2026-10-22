package fallback_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"edge-agent-go/internal/fallback"
)

func TestMessageFormatsVersionedSingleSegmentPayload(t *testing.T) {
	message, err := fallback.Format(fallback.Event{
		SiteID: "ng-kaji-01", AssetID: "batt-01", Sequence: 18422,
		Code: fallback.BatteryOverheat, TemperatureC: ptr(68.2),
		OccurredAt: time.Unix(1730000000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("Format() error = %v", err)
	}

	want := "AMBAGRID|v=1|site=ng-kaji-01|asset=batt-01|seq=18422|code=BATTERY_OVERHEAT|temp=68.2|ts=1730000000"
	if message != want {
		t.Fatalf("Format() = %q, want %q", message, want)
	}
	if len(message) > 160 {
		t.Fatalf("message length = %d, want one SMS segment", len(message))
	}
}

func TestFormatRejectsNonFiniteTemperatureAndMultipartGSM7(t *testing.T) {
	nan := math.NaN()
	base := fallback.Event{SiteID: "site-01", AssetID: "batt-01", Sequence: 1, Code: fallback.BatteryOverheat, TemperatureC: &nan, OccurredAt: time.Unix(1730000000, 0)}
	if _, err := fallback.Format(base); err == nil {
		t.Fatal("Format() accepted NaN temperature")
	}
	temperature := 68.2
	base.TemperatureC = &temperature
	base.SiteID = strings.Repeat("a", 80)
	if _, err := fallback.Format(base); err == nil {
		t.Fatal("Format() accepted a multipart GSM-7 message")
	}
}

func TestMessageRejectsUnknownCodesAndSeparators(t *testing.T) {
	tests := []fallback.Event{
		{SiteID: "site-1", AssetID: "asset-1", Sequence: 1, Code: "POWER_SPIKE", OccurredAt: time.Unix(1, 0)},
		{SiteID: "site|1", AssetID: "asset-1", Sequence: 1, Code: fallback.TamperDetected, OccurredAt: time.Unix(1, 0)},
	}
	for _, event := range tests {
		if _, err := fallback.Format(event); err == nil {
			t.Fatalf("Format(%+v) succeeded", event)
		}
	}
}

func ptr(value float64) *float64 { return &value }
