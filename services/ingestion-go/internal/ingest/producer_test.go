package ingest

import "testing"

func TestCriticalReplayHeadersUseCanonicalEdgeIdentity(t *testing.T) {
	temperature := 68.2
	headers := headersFor("ng-kaji-01", IngestRecord{Sequence: 42, CriticalCode: "BATTERY_OVERHEAT", CriticalValue: &temperature})
	values := make(map[string]string, len(headers))
	for _, header := range headers {
		values[header.Key] = string(header.Value)
	}
	if values["source_event_id"] != "edge:ng-kaji-01:42" || values["critical_code"] != "BATTERY_OVERHEAT" || values["critical_value"] != "68.2" {
		t.Fatalf("headers = %v", values)
	}
}
