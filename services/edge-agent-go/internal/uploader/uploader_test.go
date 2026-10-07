package uploader_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/uploader"
)

type fakeQueue struct {
	records    []domain.Record
	uploaded   map[uint64]time.Time
	acked      map[uint64]bool
	failed     map[uint64]string
	readyCalls atomic.Int64
}

func newFakeQueue(records []domain.Record) *fakeQueue {
	return &fakeQueue{
		records:  records,
		uploaded: make(map[uint64]time.Time),
		acked:    make(map[uint64]bool),
		failed:   make(map[uint64]string),
	}
}

func (q *fakeQueue) Persist(_ context.Context, _ domain.Event) (uint64, error) {
	return 0, nil
}

func (q *fakeQueue) MarkPendingUpload(_ context.Context, _ uint64) error {
	return nil
}

func (q *fakeQueue) Ready(_ context.Context, limit int, _ int64) ([]domain.Record, error) {
	call := q.readyCalls.Add(1)
	if call > 1 {
		return nil, nil
	}
	if limit > len(q.records) {
		limit = len(q.records)
	}
	return q.records[:limit], nil
}

func (q *fakeQueue) MarkUploaded(_ context.Context, sequence uint64, uploadedAt time.Time) error {
	q.uploaded[sequence] = uploadedAt
	return nil
}

func (q *fakeQueue) Ack(_ context.Context, sequence uint64) error {
	q.acked[sequence] = true
	return nil
}

func (q *fakeQueue) MarkFailed(_ context.Context, sequence uint64, _ time.Time, cause string) error {
	q.failed[sequence] = cause
	return nil
}

func testRecord(seq uint64) domain.Record {
	return domain.Record{
		Sequence:       seq,
		SiteID:         "site-01",
		GatewayID:      "gw-01",
		DeviceID:       "meter-001",
		AssetType:      domain.AssetSmartMeter,
		MQTTTopic:      "africa-west/site-01/smartmeter/meter-001/telemetry",
		Payload:        []byte(`{"device_id":"meter-001","device_type":"DEVICE_TYPE_SMART_METER","timestamp_utc":1700000000,"site_id":"site-01"}`),
		Priority:       domain.PriorityNormal,
		Status:         domain.StatusPendingUpload,
		EventTimestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EdgeReceivedAt: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
	}
}

func TestUploader_SuccessfulBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing or wrong auth header")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(uploader.BatchResponse{
			Accepted: []uint64{1, 2},
		})
	}))
	defer server.Close()

	queue := newFakeQueue([]domain.Record{testRecord(1), testRecord(2)})
	client := uploader.NewIngestClient(server.URL+"/v1/ingest", "test-key", 5*time.Second)
	ul := uploader.New(uploader.Config{
		BatchSize:     50,
		BatchMaxBytes: 1 << 20,
		PollInterval:  time.Hour,
		BaseDelay:     time.Second,
		MaxDelay:      time.Minute,
	}, queue, client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	if len(queue.uploaded) != 2 {
		t.Fatalf("expected 2 uploaded marks, got %d", len(queue.uploaded))
	}
	for seq, ts := range queue.uploaded {
		if ts.IsZero() {
			t.Fatalf("sequence %d has zero uploaded_at", seq)
		}
	}
	if !queue.acked[1] || !queue.acked[2] {
		t.Fatalf("expected sequences 1 and 2 acked, got %v", queue.acked)
	}
}

func TestUploader_ServerError_RetriesWithBackoff(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	queue := newFakeQueue([]domain.Record{testRecord(1)})
	client := uploader.NewIngestClient(server.URL+"/v1/ingest", "test-key", 5*time.Second)
	ul := uploader.New(uploader.Config{
		BatchSize:     50,
		BatchMaxBytes: 1 << 20,
		PollInterval:  time.Hour,
		BaseDelay:     time.Second,
		MaxDelay:      time.Minute,
	}, queue, client)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	if len(queue.failed) == 0 {
		t.Fatal("expected failed records after server error")
	}
	if _, ok := queue.failed[1]; !ok {
		t.Fatal("sequence 1 should be marked failed")
	}
}

func TestUploader_PartialRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(uploader.BatchResponse{
			Accepted: []uint64{1},
			Rejected: []uploader.RecordError{
				{Index: 1, Sequence: 2, Code: "validation_failed", Message: "bad payload"},
			},
		})
	}))
	defer server.Close()

	queue := newFakeQueue([]domain.Record{testRecord(1), testRecord(2)})
	client := uploader.NewIngestClient(server.URL+"/v1/ingest", "test-key", 5*time.Second)
	ul := uploader.New(uploader.Config{
		BatchSize:     50,
		BatchMaxBytes: 1 << 20,
		PollInterval:  time.Hour,
		BaseDelay:     time.Second,
		MaxDelay:      time.Minute,
	}, queue, client)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	if !queue.acked[1] {
		t.Fatal("sequence 1 should be acked")
	}
	if _, ok := queue.failed[2]; !ok {
		t.Fatal("sequence 2 should be marked failed")
	}
}

func TestUploader_UploadedAtTimestamp(t *testing.T) {
	before := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch struct {
			Records []struct {
				UploadedAt time.Time `json:"uploaded_at"`
			} `json:"records"`
		}
		json.NewDecoder(r.Body).Decode(&batch)
		if len(batch.Records) == 0 {
			t.Fatal("no records received")
		}
		uploadedAt := batch.Records[0].UploadedAt
		if uploadedAt.Before(before) {
			t.Errorf("uploaded_at %v is before test start %v", uploadedAt, before)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(uploader.BatchResponse{Accepted: []uint64{1}})
	}))
	defer server.Close()

	queue := newFakeQueue([]domain.Record{testRecord(1)})
	client := uploader.NewIngestClient(server.URL+"/v1/ingest", "test-key", 5*time.Second)
	ul := uploader.New(uploader.Config{
		BatchSize:     50,
		BatchMaxBytes: 1 << 20,
		PollInterval:  time.Hour,
	}, queue, client)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()
}

func TestUploader_EmptyQueue(t *testing.T) {
	queue := newFakeQueue(nil)
	client := uploader.NewIngestClient("http://localhost:0/v1/ingest", "test-key", 5*time.Second)
	ul := uploader.New(uploader.Config{
		PollInterval: time.Hour,
	}, queue, client)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()

	if len(queue.acked) != 0 {
		t.Fatal("should not ack anything from empty queue")
	}
}
