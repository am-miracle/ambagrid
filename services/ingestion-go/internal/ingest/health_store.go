package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type KafkaSiteHealthStore struct {
	client  kafkaSyncProducer
	topic   string
	timeout time.Duration
}

type kafkaSyncProducer interface {
	ProduceSync(context.Context, ...*kgo.Record) kgo.ProduceResults
}

func NewKafkaSiteHealthStore(client *kgo.Client, topic string, timeout time.Duration) *KafkaSiteHealthStore {
	return &KafkaSiteHealthStore{client: client, topic: topic, timeout: timeout}
}

func (s *KafkaSiteHealthStore) RecordContact(ctx context.Context, report SiteHealthReport) error {
	value, err := json.Marshal(siteHealthEvent{
		SiteID:               report.SiteID,
		GatewayID:            report.GatewayID,
		ContactAt:            report.ContactAt,
		LastEventTimestamp:   report.LastEventTimestamp,
		QueueDepth:           report.QueueDepth,
		OldestPendingAt:      report.OldestPendingAt,
		LastSuccessfulSyncAt: report.LastSuccessfulSyncAt,
		LastFailedSyncReason: report.LastFailedSyncReason,
	})
	if err != nil {
		return fmt.Errorf("marshal site health: %w", err)
	}

	record := &kgo.Record{
		Topic: s.topic,
		Key:   []byte(report.SiteID),
		Value: value,
	}

	produceCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	return s.client.ProduceSync(produceCtx, record).FirstErr()
}

type siteHealthEvent struct {
	SiteID               string     `json:"site_id"`
	GatewayID            string     `json:"gateway_id"`
	ContactAt            time.Time  `json:"contact_at"`
	LastEventTimestamp   *time.Time `json:"last_event_timestamp,omitempty"`
	QueueDepth           int64      `json:"queue_depth"`
	OldestPendingAt      *time.Time `json:"oldest_pending_at,omitempty"`
	LastSuccessfulSyncAt *time.Time `json:"last_successful_sync_at,omitempty"`
	LastFailedSyncReason string     `json:"last_failed_sync_reason,omitempty"`
}
