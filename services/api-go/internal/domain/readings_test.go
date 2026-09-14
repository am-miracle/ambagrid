package domain

import "testing"

func TestParseReadingMetricAcceptsEveryRegisteredMetric(t *testing.T) {
	for metric := range readingMetricNames {
		got, err := ParseReadingMetric(string(metric))
		if err != nil {
			t.Fatalf("ParseReadingMetric(%q) error = %v", metric, err)
		}
		if got != metric {
			t.Fatalf("ParseReadingMetric(%q) = %q", metric, got)
		}
	}
}

func TestParseReadingMetricRejectsUnknownMetric(t *testing.T) {
	if _, err := ParseReadingMetric("warp_core_temperature"); err == nil {
		t.Fatal("ParseReadingMetric() accepted an unregistered metric")
	}
}

func TestAggregationForMatchesSupports(t *testing.T) {
	tests := []struct {
		assetType     AssetType
		metric        ReadingMetric
		wantSupported bool
		wantAggregate ReadingAggregation
	}{
		{AssetTypeSmartMeter, ReadingMetricVoltage, true, ReadingAggregationAverage},
		{AssetTypeSmartMeter, ReadingMetricTotalKWh, true, ReadingAggregationMaximum},
		{AssetTypeBatteryBMS, ReadingMetricVoltage, false, ""},
		{AssetTypeSolarInverter, ReadingMetricSolarIrradiance, true, ReadingAggregationAverage},
	}

	for _, test := range tests {
		aggregation, ok := AggregationFor(test.assetType, test.metric)
		if ok != test.wantSupported {
			t.Fatalf("AggregationFor(%q, %q) ok = %t, want %t", test.assetType, test.metric, ok, test.wantSupported)
		}
		if aggregation != test.wantAggregate {
			t.Fatalf("AggregationFor(%q, %q) = %q, want %q", test.assetType, test.metric, aggregation, test.wantAggregate)
		}
		if test.metric.Supports(test.assetType) != test.wantSupported {
			t.Fatalf("Supports(%q) on %q = %t, want %t", test.assetType, test.metric, test.metric.Supports(test.assetType), test.wantSupported)
		}
	}
}
