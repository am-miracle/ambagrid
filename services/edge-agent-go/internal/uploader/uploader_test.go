package uploader_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/uploader"
)

type fakeQueue struct {
	mu            sync.RWMutex
	records       []domain.Record
	uploaded      map[uint64]time.Time
	acked         map[uint64]bool
	failed        map[uint64]string
	readyCalls    atomic.Int64
	uploadedCalls atomic.Int64
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
	q.uploadedCalls.Add(1)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.uploaded[sequence] = uploadedAt
	return nil
}

func (q *fakeQueue) Ack(_ context.Context, sequence uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.acked[sequence] = true
	return nil
}

func (q *fakeQueue) MarkFailed(_ context.Context, sequence uint64, _ time.Time, cause string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failed[sequence] = cause
	return nil
}

func (q *fakeQueue) Purge(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

func (q *fakeQueue) Stats(_ context.Context) (domain.QueueStats, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	var lastEvent *time.Time
	for _, record := range q.records {
		if q.acked[record.Sequence] {
			continue
		}
		if lastEvent == nil || record.EventTimestamp.After(*lastEvent) {
			t := record.EventTimestamp
			lastEvent = &t
		}
	}
	return domain.QueueStats{Depth: int64(len(q.records) - len(q.acked)), LastEventTimestamp: lastEvent}, nil
}

func (q *fakeQueue) snapshots() (map[uint64]time.Time, map[uint64]bool, map[uint64]string) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	uploaded := make(map[uint64]time.Time, len(q.uploaded))
	acked := make(map[uint64]bool, len(q.acked))
	failed := make(map[uint64]string, len(q.failed))
	for sequence, at := range q.uploaded {
		uploaded[sequence] = at
	}
	for sequence, value := range q.acked {
		acked[sequence] = value
	}
	for sequence, cause := range q.failed {
		failed[sequence] = cause
	}
	return uploaded, acked, failed
}

type fakeTracker struct {
	mu             sync.Mutex
	contactFailure string
}

func (t *fakeTracker) RecordFallbackUploadFailure(context.Context, uint64) error { return nil }
func (t *fakeTracker) RecordFallbackUploadSuccess(context.Context, uint64, time.Time) error {
	return nil
}
func (t *fakeTracker) RecordUploadContactSuccess(context.Context, time.Time) error { return nil }
func (t *fakeTracker) RecordUploadContactFailure(_ context.Context, cause string) error {
	t.mu.Lock()
	t.contactFailure = cause
	t.mu.Unlock()
	return nil
}
func (t *fakeTracker) getContactFailure() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.contactFailure
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
	}, queue, client, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	uploaded, acked, _ := queue.snapshots()
	if len(uploaded) != 2 {
		t.Fatalf("expected 2 uploaded marks, got %d", len(uploaded))
	}
	for seq, ts := range uploaded {
		if ts.IsZero() {
			t.Fatalf("sequence %d has zero uploaded_at", seq)
		}
	}
	if !acked[1] || !acked[2] {
		t.Fatalf("expected sequences 1 and 2 acked, got %v", acked)
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
	}, queue, client, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	_, _, failed := queue.snapshots()
	if len(failed) == 0 {
		t.Fatal("expected failed records after server error")
	}
	if _, ok := failed[1]; !ok {
		t.Fatal("sequence 1 should be marked failed")
	}
}

func TestUploader_DoesNotMarkUploadedBeforePublishConfirmed(t *testing.T) {
	requestArrived := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestArrived)
		<-releaseResponse
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(uploader.BatchResponse{Accepted: []uint64{1}})
	}))
	defer server.Close()

	queue := newFakeQueue([]domain.Record{testRecord(1)})
	client := uploader.NewIngestClient(server.URL+"/v1/ingest", "test-key", 5*time.Second)
	ul := uploader.New(uploader.Config{PollInterval: time.Hour}, queue, client, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ul.Run(ctx)

	select {
	case <-requestArrived:
	case <-time.After(time.Second):
		t.Fatal("upload request did not arrive")
	}
	got := queue.uploadedCalls.Load()
	close(releaseResponse)
	if got != 0 {
		t.Fatalf("MarkUploaded called %d times before the server confirmed publication", got)
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
	}, queue, client, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	_, acked, failed := queue.snapshots()
	if !acked[1] {
		t.Fatal("sequence 1 should be acked")
	}
	if _, ok := failed[2]; !ok {
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
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(uploader.BatchResponse{})
			return
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
	}, queue, client, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()
}

func TestUploader_ReplayFlag(t *testing.T) {
	var received struct {
		Records []struct {
			Sequence uint64 `json:"sequence"`
			Replay   bool   `json:"replay"`
		} `json:"records"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch struct {
			Records []struct {
				Sequence uint64 `json:"sequence"`
				Replay   bool   `json:"replay"`
			} `json:"records"`
		}
		json.NewDecoder(r.Body).Decode(&batch)
		accepted := make([]uint64, len(batch.Records))
		for i, r := range batch.Records {
			accepted[i] = r.Sequence
		}
		if len(batch.Records) > 0 {
			received = batch
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(uploader.BatchResponse{Accepted: accepted})
	}))
	defer server.Close()

	// A delayed device timestamp does not make a promptly uploaded record a replay.
	oldButPrompt := testRecord(1)
	oldButPrompt.EventTimestamp = time.Now().UTC().Add(-3 * time.Hour)
	oldButPrompt.EdgeReceivedAt = time.Now().UTC()

	// A retry carries the durable replay decision made by the queue lifecycle.
	retriedButFresh := testRecord(2)
	retriedButFresh.EventTimestamp = time.Now().UTC().Add(-30 * time.Second)
	retriedButFresh.EdgeReceivedAt = time.Now().UTC()
	retriedButFresh.AttemptCount = 1
	retriedButFresh.ReplayRequired = true

	// First-attempt records buffered behind an outage also carry that decision.
	bufferedDuringOutage := testRecord(3)
	bufferedDuringOutage.ReplayRequired = true

	queue := newFakeQueue([]domain.Record{oldButPrompt, retriedButFresh, bufferedDuringOutage})
	client := uploader.NewIngestClient(server.URL+"/v1/ingest", "test-key", 5*time.Second)
	ul := uploader.New(uploader.Config{
		BatchSize:     50,
		BatchMaxBytes: 1 << 20,
		PollInterval:  time.Hour,
	}, queue, client, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	if len(received.Records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(received.Records))
	}
	if received.Records[0].Replay {
		t.Error("record uploaded on its first attempt should have replay=false despite an old device timestamp")
	}
	if !received.Records[1].Replay {
		t.Error("record uploaded after a failed attempt should have replay=true despite a short delay")
	}
	if !received.Records[2].Replay {
		t.Error("first-attempt record buffered during outage should have replay=true")
	}
}

func TestUploader_ExhaustedRetries_DropsRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer server.Close()

	exhausted := testRecord(1)
	exhausted.AttemptCount = 4

	queue := newFakeQueue([]domain.Record{exhausted})
	client := uploader.NewIngestClient(server.URL+"/v1/ingest", "test-key", 5*time.Second)
	ul := uploader.New(uploader.Config{
		BatchSize:     50,
		BatchMaxBytes: 1 << 20,
		PollInterval:  time.Hour,
		BaseDelay:     time.Second,
		MaxDelay:      time.Minute,
		MaxRetries:    5,
	}, queue, client, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(500 * time.Millisecond)
	cancel()

	_, acked, failed := queue.snapshots()
	if !acked[1] {
		t.Fatal("exhausted record should be acked (dropped)")
	}
	if _, ok := failed[1]; ok {
		t.Fatal("exhausted record should not be marked for retry")
	}
}

func TestUploader_EmptyQueue(t *testing.T) {
	queue := newFakeQueue(nil)
	client := uploader.NewIngestClient("http://localhost:0/v1/ingest", "test-key", 5*time.Second)
	ul := uploader.New(uploader.Config{
		PollInterval: time.Hour,
	}, queue, client, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()

	_, acked, _ := queue.snapshots()
	if len(acked) != 0 {
		t.Fatal("should not ack anything from empty queue")
	}
}

func TestUploader_EmptyHeartbeatFailure_RecordsContactFailure(t *testing.T) {
	queue := newFakeQueue(nil)
	client := uploader.NewIngestClient("http://localhost:0/v1/ingest", "test-key", 5*time.Second)
	tracker := &fakeTracker{}
	ul := uploader.New(uploader.Config{
		PollInterval: time.Hour,
	}, queue, client, tracker)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go ul.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()

	if tracker.getContactFailure() == "" {
		t.Fatal("empty-batch heartbeat failure should record contact failure on the tracker")
	}
}
