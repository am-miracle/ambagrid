package ingest

import (
	"context"
	"fmt"
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

func (p *KafkaProducer) Produce(ctx context.Context, siteID string, rec IngestRecord) error {
	value, err := transcodePayload(rec.Payload)
	if err != nil {
		return fmt.Errorf("transcode payload: %w", err)
	}

	record := &kgo.Record{
		Topic: p.topic,
		Key:   []byte(rec.DeviceID),
		Value: value,
		Headers: []kgo.RecordHeader{
			{Key: "mqtt_topic", Value: []byte(rec.MQTTTopic)},
			{Key: "region", Value: regionFromTopic(rec.MQTTTopic)},
			{Key: "site_id", Value: []byte(siteID)},
			{Key: "device_type", Value: []byte(rec.AssetType)},
			{Key: "device_id", Value: []byte(rec.DeviceID)},
			{Key: "source", Value: []byte("http")},
		},
	}

	produceCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	return p.client.ProduceSync(produceCtx, record).FirstErr()
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
	idx := 0
	for i, c := range mqttTopic {
		if c == '/' {
			return []byte(mqttTopic[:i])
		}
		idx = i
	}
	_ = idx
	return []byte(mqttTopic)
}
