package ingest

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	telemetrypb "ingestion-go/proto/ambagrid/telemetry"
)

type KafkaProducer struct {
	client  *kgo.Client
	topic   string
	timeout time.Duration
}

func NewKafkaProducer(client *kgo.Client, topic string, timeout time.Duration) *KafkaProducer {
	return &KafkaProducer{client: client, topic: topic, timeout: timeout}
}

// Produce transcodes the JSON payload to the same binary protobuf the MQTT
// bridge emits, so downstream consumers see a uniform wire format regardless
// of whether the record arrived via MQTT or HTTP.
func (p *KafkaProducer) Produce(ctx context.Context, siteID string, rec IngestRecord) error {
	value, err := transcodePayload(rec.Payload)
	if err != nil {
		return fmt.Errorf("transcode payload: %w", err)
	}

	headers := headersFor(siteID, rec)
	record := &kgo.Record{
		Topic:   p.topic,
		Key:     []byte(rec.DeviceID),
		Value:   value,
		Headers: headers,
	}

	produceCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	return p.client.ProduceSync(produceCtx, record).FirstErr()
}

func headersFor(siteID string, rec IngestRecord) []kgo.RecordHeader {
	headers := []kgo.RecordHeader{
		{Key: "mqtt_topic", Value: []byte(rec.MQTTTopic)},
		{Key: "region", Value: regionFromTopic(rec.MQTTTopic)},
		{Key: "site_id", Value: []byte(siteID)},
		{Key: "gateway_id", Value: []byte(rec.GatewayID)},
		{Key: "device_type", Value: []byte(rec.AssetType)},
		{Key: "device_id", Value: []byte(rec.DeviceID)},
		{Key: "sequence", Value: []byte(strconv.FormatUint(rec.Sequence, 10))},
		{Key: "source_event_id", Value: []byte(fmt.Sprintf("edge:%s:%d", siteID, rec.Sequence))},
		{Key: "event_timestamp", Value: []byte(rec.EventTimestamp.UTC().Format(time.RFC3339Nano))},
		{Key: "edge_received_at", Value: []byte(rec.EdgeReceivedAt.UTC().Format(time.RFC3339Nano))},
		{Key: "uploaded_at", Value: []byte(rec.UploadedAt.UTC().Format(time.RFC3339Nano))},
		{Key: "replay", Value: []byte(strconv.FormatBool(rec.Replay))},
		{Key: "source", Value: []byte("http")},
	}
	if rec.CriticalCode != "" {
		headers = append(headers, kgo.RecordHeader{Key: "critical_code", Value: []byte(rec.CriticalCode)})
	}
	if rec.CriticalValue != nil {
		headers = append(headers, kgo.RecordHeader{Key: "critical_value", Value: []byte(strconv.FormatFloat(*rec.CriticalValue, 'f', -1, 64))})
	}
	return headers
}

func transcodePayload(payload []byte) ([]byte, error) {
	msg := &telemetrypb.MetricPayload{}
	if err := protojson.Unmarshal(payload, msg); err != nil {
		return nil, err
	}
	if msg.TimestampUtc == nil || msg.GetTimestampUtc() == 0 {
		return nil, fmt.Errorf("timestamp_utc must be set in payload")
	}
	return proto.Marshal(msg)
}

func regionFromTopic(mqttTopic string) []byte {
	for i, c := range mqttTopic {
		if c == '/' {
			return []byte(mqttTopic[:i])
		}
	}
	return []byte(mqttTopic)
}
