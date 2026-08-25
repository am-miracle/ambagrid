// Package telemetry parses the MQTT topic hierarchy for hardware telemetry
// and builds the Kafka record that carries it downstream.
package telemetry

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
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

// BuildRecord preserves the hardware payload verbatim and attaches routing
// context as Kafka metadata. payload is used as-is; callers own it and must
// not mutate it after this call.
func BuildRecord(kafkaTopic, mqttTopic string, payload []byte, metadata MessageMetadata, constraints TopicConstraints) (*kgo.Record, error) {
	topicMeta, err := ParseTopic(mqttTopic, constraints)
	if err != nil {
		return nil, err
	}

	// The device ID is the key so all readings from a meter stay ordered on the same partition.
	return &kgo.Record{
		Topic: kafkaTopic,
		Key:   []byte(topicMeta.DeviceID),
		Value: payload,
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
