package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
)

type TelemetryTopic struct {
	Region     string
	SiteID     string
	DeviceType string
	DeviceID   string
}

type MQTTMessageMetadata struct {
	Duplicate bool
	QOS       byte
	Retained  bool
	MessageID uint16
}

func ParseTelemetryTopic(topic string) (TelemetryTopic, error) {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 {
		return TelemetryTopic{}, fmt.Errorf("telemetry topic must have 5 segments: %q", topic)
	}
	if parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" || parts[4] == "" {
		return TelemetryTopic{}, fmt.Errorf("telemetry topic contains empty segment: %q", topic)
	}
	if parts[4] != "telemetry" {
		return TelemetryTopic{}, fmt.Errorf("telemetry topic must end with telemetry: %q", topic)
	}
	if parts[0] != "africa-west" {
		return TelemetryTopic{}, fmt.Errorf("telemetry topic region must be africa-west: %q", topic)
	}
	if parts[2] != "smartmeter" {
		return TelemetryTopic{}, fmt.Errorf("telemetry topic device type must be smartmeter: %q", topic)
	}

	return TelemetryTopic{
		Region:     parts[0],
		SiteID:     parts[1],
		DeviceType: parts[2],
		DeviceID:   parts[3],
	}, nil
}

func BuildTelemetryRecord(kafkaTopic string, mqttTopic string, payload []byte, metadata MQTTMessageMetadata) (*kgo.Record, error) {
	topicMeta, err := ParseTelemetryTopic(mqttTopic)
	if err != nil {
		return nil, err
	}

	return &kgo.Record{
		Topic: kafkaTopic,
		Key:   []byte(topicMeta.DeviceID),
		Value: append([]byte(nil), payload...),
		Headers: []kgo.RecordHeader{
			{Key: "mqtt_topic", Value: []byte(mqttTopic)},
			{Key: "mqtt_qos", Value: []byte(strconv.Itoa(int(metadata.QOS)))},
			{Key: "mqtt_retained", Value: []byte(strconv.FormatBool(metadata.Retained))},
			{Key: "mqtt_duplicate", Value: []byte(strconv.FormatBool(metadata.Duplicate))},
			{Key: "mqtt_message_id", Value: []byte(strconv.Itoa(int(metadata.MessageID)))},
			{Key: "region", Value: []byte(topicMeta.Region)},
			{Key: "site_id", Value: []byte(topicMeta.SiteID)},
			{Key: "device_type", Value: []byte(topicMeta.DeviceType)},
			{Key: "device_id", Value: []byte(topicMeta.DeviceID)},
		},
	}, nil
}
