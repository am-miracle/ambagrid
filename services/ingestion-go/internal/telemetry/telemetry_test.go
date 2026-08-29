package telemetry

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	telemetrypb "ingestion-go/proto/ambagrid/telemetry"
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
	payload := []byte(`{
		"device_id": "met-0101",
		"device_type": "DEVICE_TYPE_SMART_METER",
		"site_id": "ng-kaji-01",
		"metrics": {"voltage": 231.0}
	}`)

	record, err := BuildRecord("telemetry.ingested", "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry", payload, MessageMetadata{
		Duplicate: true,
		QOS:       1,
		Retained:  true,
		MessageID: 42,
	}, TopicConstraints{Region: "africa-west", DeviceType: "smartmeter"})
	if err != nil {
		t.Fatalf("BuildRecord returned error: %v", err)
	}

	if record.Topic != "telemetry.ingested" {
		t.Fatalf("Topic = %q, want %q", record.Topic, "telemetry.ingested")
	}
	if string(record.Key) != "met-0101" {
		t.Fatalf("Key = %q, want %q", string(record.Key), "met-0101")
	}

	var decoded telemetrypb.MetricPayload
	if err := proto.Unmarshal(record.Value, &decoded); err != nil {
		t.Fatalf("record.Value did not decode as MetricPayload protobuf: %v", err)
	}
	if decoded.GetDeviceId() != "met-0101" {
		t.Fatalf("decoded DeviceId = %q, want %q", decoded.GetDeviceId(), "met-0101")
	}
	if decoded.GetDeviceType() != telemetrypb.DeviceType_DEVICE_TYPE_SMART_METER {
		t.Fatalf("decoded DeviceType = %v, want %v", decoded.GetDeviceType(), telemetrypb.DeviceType_DEVICE_TYPE_SMART_METER)
	}
	if decoded.GetSiteId() != "ng-kaji-01" {
		t.Fatalf("decoded SiteId = %q, want %q", decoded.GetSiteId(), "ng-kaji-01")
	}
	if decoded.GetMetrics().GetVoltage() != 231.0 {
		t.Fatalf("decoded Metrics.Voltage = %v, want %v", decoded.GetMetrics().GetVoltage(), 231.0)
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

func TestBuildRecordRejectsMalformedPayload(t *testing.T) {
	payload := []byte(`{"device_id": "met-0101"`) // truncated JSON

	_, err := BuildRecord("telemetry.ingested", "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry", payload, MessageMetadata{}, TopicConstraints{Region: "africa-west", DeviceType: "smartmeter"})
	if err == nil {
		t.Fatal("BuildRecord returned nil error for malformed telemetry payload")
	}
}

func TestBuildRecordRejectsUnknownDeviceTypeName(t *testing.T) {
	payload := []byte(`{"device_id": "met-0101", "device_type": "DEVICE_TYPE_NOT_REAL"}`)

	_, err := BuildRecord("telemetry.ingested", "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry", payload, MessageMetadata{}, TopicConstraints{Region: "africa-west", DeviceType: "smartmeter"})
	if err == nil {
		t.Fatal("BuildRecord returned nil error for an unknown device_type enum name")
	}
}

func TestBuildDeadLetterRecordPreservesOriginalPayload(t *testing.T) {
	payload := []byte(`{"device_id": "met-0101"`) // truncated JSON
	cause := errors.New("decode telemetry payload: unexpected end of JSON input")

	record := BuildDeadLetterRecord("telemetry.ingested.dlq", "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry", payload, MessageMetadata{
		QOS:       1,
		Retained:  true,
		Duplicate: true,
		MessageID: 7,
	}, cause)

	if record.Topic != "telemetry.ingested.dlq" {
		t.Fatalf("Topic = %q, want %q", record.Topic, "telemetry.ingested.dlq")
	}
	if string(record.Value) != string(payload) {
		t.Fatalf("Value = %q, want original payload untouched: %q", record.Value, payload)
	}
	if string(record.Key) != "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry" {
		t.Fatalf("Key = %q, want raw mqtt topic", record.Key)
	}

	headers := headersByKey(record.Headers)
	if headers["error"] != cause.Error() {
		t.Fatalf("error header = %q, want %q", headers["error"], cause.Error())
	}
	if headers["mqtt_topic"] != "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry" {
		t.Fatalf("mqtt_topic header = %q", headers["mqtt_topic"])
	}
	if headers["mqtt_qos"] != "1" {
		t.Fatalf("mqtt_qos header = %q", headers["mqtt_qos"])
	}
	if headers["mqtt_message_id"] != "7" {
		t.Fatalf("mqtt_message_id header = %q", headers["mqtt_message_id"])
	}
}

func TestBuildDeadLetterRecordHandlesMalformedTopic(t *testing.T) {
	// A record ParseTopic itself would reject must still produce a DLQ record,
	// since BuildRecord can fail on the topic before ever reaching the payload.
	record := BuildDeadLetterRecord("telemetry.ingested.dlq", "not-a-valid-topic", []byte("payload"), MessageMetadata{}, errors.New("boom"))

	if string(record.Key) != "not-a-valid-topic" {
		t.Fatalf("Key = %q, want raw mqtt topic", record.Key)
	}
	if string(record.Value) != "payload" {
		t.Fatalf("Value = %q, want original payload", record.Value)
	}
}

func headersByKey(headers []kgo.RecordHeader) map[string]string {
	got := make(map[string]string, len(headers))
	for _, header := range headers {
		got[header.Key] = string(header.Value)
	}
	return got
}
