// Package telemetry parses the MQTT topic hierarchy for hardware telemetry
// and builds the Kafka record that carries it downstream.
package telemetry

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	telemetrypb "ingestion-go/proto/ambagrid/telemetry"
)

// routing metadata encoded in the MQTT topic hierarchy.
type Topic struct {
	Region     string
	SiteID     string
	DeviceType string
	DeviceID   string
}

// carries broker-level delivery flags into Kafka headers.
type MessageMetadata struct {
	Duplicate bool
	QOS       byte
	Retained  bool
	MessageID uint16
}

// non-wildcard segments of the configured MQTT
// subscription filter that incoming topics are validated against. An empty
// field means that segment was wildcarded and any value is accepted.
type TopicConstraints struct {
	Region     string
	SiteID     string
	DeviceType string
	DeviceID   string
}

type topicSegments struct {
	Region, SiteID, DeviceType, DeviceID, Suffix string
}

func splitTopicSegments(s string) (topicSegments, bool) {
	parts := strings.Split(s, "/")
	if len(parts) != 5 {
		return topicSegments{}, false
	}
	return topicSegments{
		Region:     parts[0],
		SiteID:     parts[1],
		DeviceType: parts[2],
		DeviceID:   parts[3],
		Suffix:     parts[4],
	}, true
}

// DeriveConstraints reads the non-wildcard segments out of a valid MQTT
// subscription filter, e.g. "africa-west/+/smartmeter/+/telemetry" yields a
// region and device-type constraint but leaves site and device unconstrained.
func DeriveConstraints(filter string) (TopicConstraints, error) {
	segs, ok := splitTopicSegments(filter)
	if !ok {
		return TopicConstraints{}, fmt.Errorf("MQTT_TOPIC_FILTER must have 5 segments: %q", filter)
	}
	if err := validateFilterSegments(filter, segs); err != nil {
		return TopicConstraints{}, err
	}
	return TopicConstraints{
		Region:     nonWildcard(segs.Region),
		SiteID:     nonWildcard(segs.SiteID),
		DeviceType: nonWildcard(segs.DeviceType),
		DeviceID:   nonWildcard(segs.DeviceID),
	}, nil
}

// ValidateFilter ensures the configured subscription filter matches the topic
// shape this ingestion bridge can validate.
func ValidateFilter(filter string) error {
	segs, ok := splitTopicSegments(filter)
	if !ok {
		return fmt.Errorf("MQTT_TOPIC_FILTER must have 5 segments: %q", filter)
	}
	return validateFilterSegments(filter, segs)
}

func validateFilterSegments(filter string, segs topicSegments) error {
	if segs.Region == "" || segs.SiteID == "" || segs.DeviceType == "" || segs.DeviceID == "" || segs.Suffix == "" {
		return fmt.Errorf("MQTT_TOPIC_FILTER contains empty segment: %q", filter)
	}
	if segs.Suffix != "telemetry" {
		return fmt.Errorf("MQTT_TOPIC_FILTER must end with telemetry: %q", filter)
	}
	return nil
}

func nonWildcard(seg string) string {
	if seg == "+" || seg == "#" {
		return ""
	}
	return seg
}

// ParseTopic validates the ingestion boundary before trusting topic-derived
// metadata.
func ParseTopic(topic string, constraints TopicConstraints) (Topic, error) {
	segs, ok := splitTopicSegments(topic)
	if !ok {
		return Topic{}, fmt.Errorf("telemetry topic must have 5 segments: %q", topic)
	}
	if segs.Region == "" || segs.SiteID == "" || segs.DeviceType == "" || segs.DeviceID == "" || segs.Suffix == "" {
		return Topic{}, fmt.Errorf("telemetry topic contains empty segment: %q", topic)
	}
	if segs.Suffix != "telemetry" {
		return Topic{}, fmt.Errorf("telemetry topic must end with telemetry: %q", topic)
	}
	if err := constraints.violation(topic, segs); err != nil {
		return Topic{}, err
	}

	return Topic{
		Region:     segs.Region,
		SiteID:     segs.SiteID,
		DeviceType: segs.DeviceType,
		DeviceID:   segs.DeviceID,
	}, nil
}

// jsonToMetricPayload parses the hardware's JSON telemetry payload and
// re-encodes it as the binary MetricPayload protobuf defined in
// proto/telemetry.proto. protojson accepts both the proto's original
// snake_case field names and enum name strings, which is what the simulated
// and real edge hardware send.
func jsonToMetricPayload(payload []byte) ([]byte, error) {
	msg := &telemetrypb.MetricPayload{}
	if err := protojson.Unmarshal(payload, msg); err != nil {
		return nil, err
	}
	if msg.TimestampUtc == nil || msg.GetTimestampUtc() == 0 {
		return nil, fmt.Errorf("timestamp_utc must be set")
	}
	return proto.Marshal(msg)
}

// violation reports the first constrained segment that doesn't match, if any.
func (c TopicConstraints) violation(topic string, segs topicSegments) error {
	checks := []struct{ name, want, got string }{
		{"region", c.Region, segs.Region},
		{"site", c.SiteID, segs.SiteID},
		{"device type", c.DeviceType, segs.DeviceType},
		{"device", c.DeviceID, segs.DeviceID},
	}
	for _, ck := range checks {
		if ck.want != "" && ck.got != ck.want {
			return fmt.Errorf("telemetry topic %s must be %s: %q", ck.name, ck.want, topic)
		}
	}
	return nil
}

// BuildDeadLetterRecord preserves a message that BuildRecord rejected so it
// can be inspected or replayed once the cause is fixed. Unlike BuildRecord,
// this never fails: mqttTopic may itself be the reason BuildRecord failed, so
// this keys on the raw MQTT topic string instead of parsed routing metadata,
// and carries the original payload untouched rather than a transcoded value.
func BuildDeadLetterRecord(dlqTopic, mqttTopic string, payload []byte, metadata MessageMetadata, cause error) *kgo.Record {
	return &kgo.Record{
		Topic: dlqTopic,
		Key:   []byte(mqttTopic),
		Value: payload,
		Headers: []kgo.RecordHeader{
			{Key: "mqtt_topic", Value: []byte(mqttTopic)},
			{Key: "mqtt_qos", Value: []byte(strconv.Itoa(int(metadata.QOS)))},
			{Key: "mqtt_retained", Value: []byte(strconv.FormatBool(metadata.Retained))},
			{Key: "mqtt_duplicate", Value: []byte(strconv.FormatBool(metadata.Duplicate))},
			{Key: "mqtt_message_id", Value: []byte(strconv.Itoa(int(metadata.MessageID)))},
			{Key: "error", Value: []byte(cause.Error())},
		},
	}
}

// BuildRecord translates the hardware's JSON payload into the wire-format
// MetricPayload protobuf the downstream engine decodes, and attaches routing
// context as Kafka metadata.
func BuildRecord(kafkaTopic, mqttTopic string, payload []byte, metadata MessageMetadata, constraints TopicConstraints) (*kgo.Record, error) {
	topicMeta, err := ParseTopic(mqttTopic, constraints)
	if err != nil {
		return nil, err
	}

	value, err := jsonToMetricPayload(payload)
	if err != nil {
		return nil, fmt.Errorf("decode telemetry payload: %w", err)
	}

	// The device ID is the key so all readings from a meter stay ordered on the same partition.
	return &kgo.Record{
		Topic: kafkaTopic,
		Key:   []byte(topicMeta.DeviceID),
		Value: value,
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
