package telemetry

import (
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestDeriveConstraints(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		want   TopicConstraints
	}{
		{"default filter", "africa-west/+/smartmeter/+/telemetry", TopicConstraints{Region: "africa-west", DeviceType: "smartmeter"}},
		{"non-default filter", "africa-east/+/inverter/+/telemetry", TopicConstraints{Region: "africa-east", DeviceType: "inverter"}},
		{"fully wildcarded filter", "+/+/+/+/telemetry", TopicConstraints{}},
		{"pinned to one site and device", "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry", TopicConstraints{Region: "africa-west", SiteID: "ng-kaji-01", DeviceType: "smartmeter", DeviceID: "met-0101"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeriveConstraints(tt.filter)
			if err != nil {
				t.Fatalf("DeriveConstraints(%q) returned error: %v", tt.filter, err)
			}
			if got != tt.want {
				t.Fatalf("DeriveConstraints(%q) = %+v, want %+v", tt.filter, got, tt.want)
			}
		})
	}
}

func TestDeriveConstraintsRejectsMalformedFilter(t *testing.T) {
	if _, err := DeriveConstraints("africa-west/ng-kaji-01/met-0101"); err == nil {
		t.Fatal("DeriveConstraints returned nil error for malformed filter")
	}
}

func TestValidateFilter(t *testing.T) {
	tests := []string{
		"africa-west/+/smartmeter/+/telemetry",
		"+/+/+/+/telemetry",
		"#/#/#/#/telemetry",
		"africa-west/ng-kaji-01/smartmeter/met-0101/telemetry",
	}

	for _, test := range tests {
		if err := ValidateFilter(test); err != nil {
			t.Fatalf("ValidateFilter(%q) returned error: %v", test, err)
		}
	}
}

func TestValidateFilterRejectsMalformedFilter(t *testing.T) {
	tests := []string{
		"africa-west/ng-kaji-01/met-0101",
		"africa-west//smartmeter/+/telemetry",
		"africa-west/+/smartmeter/+/events",
	}

	for _, test := range tests {
		if err := ValidateFilter(test); err == nil {
			t.Fatalf("ValidateFilter(%q) returned nil error", test)
		}
	}
}

func TestParseTopic(t *testing.T) {
	meta, err := ParseTopic("africa-west/ng-kaji-01/smartmeter/met-0101/telemetry", TopicConstraints{Region: "africa-west", DeviceType: "smartmeter"})
	if err != nil {
		t.Fatalf("ParseTopic returned error: %v", err)
	}

	if meta.Region != "africa-west" {
		t.Fatalf("Region = %q, want %q", meta.Region, "africa-west")
	}
	if meta.SiteID != "ng-kaji-01" {
		t.Fatalf("SiteID = %q, want %q", meta.SiteID, "ng-kaji-01")
	}
	if meta.DeviceType != "smartmeter" {
		t.Fatalf("DeviceType = %q, want %q", meta.DeviceType, "smartmeter")
	}
	if meta.DeviceID != "met-0101" {
		t.Fatalf("DeviceID = %q, want %q", meta.DeviceID, "met-0101")
	}
}

func TestParseTopicWildcardSegmentsAcceptAnyValue(t *testing.T) {
	meta, err := ParseTopic("europe-west/ng-kaji-01/inverter/met-0101/telemetry", TopicConstraints{})
	if err != nil {
		t.Fatalf("ParseTopic returned error: %v", err)
	}
	if meta.Region != "europe-west" || meta.DeviceType != "inverter" {
		t.Fatalf("unexpected topic metadata: %+v", meta)
	}
}

func TestParseTopicRejectsMalformedTopic(t *testing.T) {
	tests := []string{
		"africa-west/ng-kaji-01/met-0101",
		"europe-west/ng-kaji-01/smartmeter/met-0101/telemetry",
		"africa-west/ng-kaji-01/inverter/met-0101/telemetry",
	}

	constraints := TopicConstraints{Region: "africa-west", DeviceType: "smartmeter"}
	for _, test := range tests {
		if _, err := ParseTopic(test, constraints); err == nil {
			t.Fatalf("ParseTopic returned nil error for malformed topic %q", test)
		}
	}
}

func TestParseTopicRejectsSiteAndDeviceMismatch(t *testing.T) {
	constraints := TopicConstraints{Region: "africa-west", SiteID: "ng-kaji-01", DeviceType: "smartmeter", DeviceID: "met-0101"}

	if _, err := ParseTopic("africa-west/ng-kaji-02/smartmeter/met-0101/telemetry", constraints); err == nil {
		t.Fatal("ParseTopic returned nil error for a topic under a different site")
	}
	if _, err := ParseTopic("africa-west/ng-kaji-01/smartmeter/met-9999/telemetry", constraints); err == nil {
		t.Fatal("ParseTopic returned nil error for a topic under a different device")
	}
	if _, err := ParseTopic("africa-west/ng-kaji-01/smartmeter/met-0101/telemetry", constraints); err != nil {
		t.Fatalf("ParseTopic returned error for a topic matching all constraints: %v", err)
	}
}

func TestBuildRecord(t *testing.T) {
	payload := []byte(`{"device_id":"met-0101","site_id":"ng-kaji-01"}`)

	record, err := BuildRecord("telemetry.raw", "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry", payload, MessageMetadata{
		Duplicate: true,
		QOS:       1,
		Retained:  true,
		MessageID: 42,
	}, TopicConstraints{Region: "africa-west", DeviceType: "smartmeter"})
	if err != nil {
		t.Fatalf("BuildRecord returned error: %v", err)
	}

	if record.Topic != "telemetry.raw" {
		t.Fatalf("Topic = %q, want %q", record.Topic, "telemetry.raw")
	}
	if string(record.Key) != "met-0101" {
		t.Fatalf("Key = %q, want %q", string(record.Key), "met-0101")
	}
	if string(record.Value) != string(payload) {
		t.Fatalf("Value = %q, want %q", string(record.Value), string(payload))
	}

	headers := headersByKey(record.Headers)
	if headers["mqtt_topic"] != "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry" {
		t.Fatalf("mqtt_topic header = %q", headers["mqtt_topic"])
	}
	if headers["mqtt_qos"] != "1" {
		t.Fatalf("mqtt_qos header = %q", headers["mqtt_qos"])
	}
	if headers["mqtt_retained"] != "true" {
		t.Fatalf("mqtt_retained header = %q", headers["mqtt_retained"])
	}
	if headers["mqtt_duplicate"] != "true" {
		t.Fatalf("mqtt_duplicate header = %q", headers["mqtt_duplicate"])
	}
	if headers["mqtt_message_id"] != "42" {
		t.Fatalf("mqtt_message_id header = %q", headers["mqtt_message_id"])
	}
	if headers["site_id"] != "ng-kaji-01" {
		t.Fatalf("site_id header = %q", headers["site_id"])
	}
}

func headersByKey(headers []kgo.RecordHeader) map[string]string {
	got := make(map[string]string, len(headers))
	for _, header := range headers {
		got[header.Key] = string(header.Value)
	}
	return got
}
