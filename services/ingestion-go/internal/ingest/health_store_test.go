package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestSiteHealthEventJSONContract(t *testing.T) {
	lastEvent := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	oldestPending := time.Date(2026, 10, 15, 9, 55, 0, 0, time.UTC)
	lastSync := time.Date(2026, 10, 15, 9, 58, 0, 0, time.UTC)

	event := siteHealthEvent{
		SiteID:               "ng-kaji-01",
		GatewayID:            "gw-01",
		ContactAt:            time.Date(2026, 10, 15, 10, 0, 5, 0, time.UTC),
		LastEventTimestamp:   &lastEvent,
		QueueDepth:           42,
		OldestPendingAt:      &oldestPending,
		LastSuccessfulSyncAt: &lastSync,
		LastFailedSyncReason: "connection refused",
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	required := []string{"site_id", "gateway_id", "contact_at", "last_event_timestamp", "queue_depth", "oldest_pending_at", "last_successful_sync_at", "last_failed_sync_reason"}
	for _, key := range required {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing required field %q", key)
		}
	}

	if decoded["site_id"] != "ng-kaji-01" {
		t.Errorf("site_id = %v", decoded["site_id"])
	}
	if decoded["gateway_id"] != "gw-01" {
		t.Errorf("gateway_id = %v", decoded["gateway_id"])
	}
	if int64(decoded["queue_depth"].(float64)) != 42 {
		t.Errorf("queue_depth = %v", decoded["queue_depth"])
	}
	if decoded["last_failed_sync_reason"] != "connection refused" {
		t.Errorf("last_failed_sync_reason = %v", decoded["last_failed_sync_reason"])
	}
}

func TestSiteHealthEventOmitsNilOptionalFields(t *testing.T) {
	event := siteHealthEvent{
		SiteID:    "ng-kaji-01",
		GatewayID: "gw-01",
		ContactAt: time.Date(2026, 10, 15, 10, 0, 5, 0, time.UTC),
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	omitted := []string{"last_event_timestamp", "oldest_pending_at", "last_successful_sync_at", "last_failed_sync_reason"}
	for _, key := range omitted {
		if value, ok := decoded[key]; ok {
			t.Errorf("nil field %q should be omitted, got %v", key, value)
		}
	}
}

type recordingSyncProducer struct {
	ctx     context.Context
	records []*kgo.Record
	err     error
}

func TestNewKafkaSiteHealthStore(t *testing.T) {
	store := NewKafkaSiteHealthStore(nil, "site-health", 5*time.Second)
	if store.topic != "site-health" || store.timeout != 5*time.Second {
		t.Fatalf("store topic/timeout = %q/%v", store.topic, store.timeout)
	}
}

func (p *recordingSyncProducer) ProduceSync(ctx context.Context, records ...*kgo.Record) kgo.ProduceResults {
	p.ctx = ctx
	p.records = append(p.records, records...)
	results := make(kgo.ProduceResults, len(records))
	for i, record := range records {
		results[i] = kgo.ProduceResult{Record: record, Err: p.err}
	}
	return results
}

func TestRecordContactPublishesSiteHealthEvent(t *testing.T) {
	producer := &recordingSyncProducer{}
	store := &KafkaSiteHealthStore{client: producer, topic: "site-health", timeout: 5 * time.Second}
	contactAt := time.Date(2026, 10, 15, 10, 0, 5, 0, time.UTC)

	if err := store.RecordContact(context.Background(), SiteHealthReport{
		SiteID: "ng-kaji-01", GatewayID: "gw-01", ContactAt: contactAt, QueueDepth: 42,
	}); err != nil {
		t.Fatalf("RecordContact() error = %v", err)
	}
	if len(producer.records) != 1 {
		t.Fatalf("produced records = %d, want 1", len(producer.records))
	}
	record := producer.records[0]
	if record.Topic != "site-health" || string(record.Key) != "ng-kaji-01" {
		t.Fatalf("record topic/key = %q/%q", record.Topic, record.Key)
	}
	var event siteHealthEvent
	if err := json.Unmarshal(record.Value, &event); err != nil {
		t.Fatalf("decode produced event: %v", err)
	}
	if event.SiteID != "ng-kaji-01" || event.GatewayID != "gw-01" || event.QueueDepth != 42 || !event.ContactAt.Equal(contactAt) {
		t.Fatalf("produced event = %+v", event)
	}
	deadline, ok := producer.ctx.Deadline()
	if !ok {
		t.Fatal("producer context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 5*time.Second {
		t.Fatalf("producer deadline remaining = %v, want within 5s", remaining)
	}
}

func TestRecordContactReturnsProducerError(t *testing.T) {
	want := errors.New("kafka unavailable")
	producer := &recordingSyncProducer{err: want}
	store := &KafkaSiteHealthStore{client: producer, topic: "site-health", timeout: time.Second}

	err := store.RecordContact(context.Background(), SiteHealthReport{SiteID: "site-01"})
	if !errors.Is(err, want) {
		t.Fatalf("RecordContact() error = %v, want %v", err, want)
	}
}
