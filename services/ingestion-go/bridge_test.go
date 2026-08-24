package main

import (
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestParseTelemetryTopic(t *testing.T) {
	meta, err := ParseTelemetryTopic("africa-west/ng-kaji-01/smartmeter/met-0101/telemetry")
	if err != nil {
		t.Fatalf("ParseTelemetryTopic returned error: %v", err)
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

func TestParseTelemetryTopicRejectsMalformedTopic(t *testing.T) {
	tests := []string{
		"africa-west/ng-kaji-01/met-0101",
		"europe-west/ng-kaji-01/smartmeter/met-0101/telemetry",
		"africa-west/ng-kaji-01/inverter/met-0101/telemetry",
	}

	for _, test := range tests {
		if _, err := ParseTelemetryTopic(test); err == nil {
			t.Fatalf("ParseTelemetryTopic returned nil error for malformed topic %q", test)
		}
	}
}

func TestBuildTelemetryRecord(t *testing.T) {
	payload := []byte(`{"device_id":"met-0101","site_id":"ng-kaji-01"}`)

	record, err := BuildTelemetryRecord("telemetry.raw", "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry", payload, MQTTMessageMetadata{
		Duplicate: true,
		QOS:       1,
		Retained:  true,
		MessageID: 42,
	})
	if err != nil {
		t.Fatalf("BuildTelemetryRecord returned error: %v", err)
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
