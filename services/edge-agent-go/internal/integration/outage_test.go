//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/repository/sqlite"
	"edge-agent-go/internal/uploader"
)

type testWireRecord struct {
	Sequence       uint64    `json:"sequence"`
	SiteID         string    `json:"site_id"`
	GatewayID      string    `json:"gateway_id"`
	DeviceID       string    `json:"device_id"`
	AssetType      string    `json:"asset_type"`
	MQTTTopic      string    `json:"mqtt_topic"`
	Payload        []byte    `json:"payload"`
	Priority       int       `json:"priority"`
	EventTimestamp time.Time `json:"event_timestamp"`
	EdgeReceivedAt time.Time `json:"edge_received_at"`
	UploadedAt     time.Time `json:"uploaded_at"`
	Replay         bool      `json:"replay"`
	CriticalCode   string    `json:"critical_code,omitempty"`
	CriticalValue  *float64  `json:"critical_value,omitempty"`
}

type testBatchRequest struct {
	GatewayID          string           `json:"gateway_id"`
	QueueDepth         int64            `json:"queue_depth"`
	OldestPendingAt    *time.Time       `json:"oldest_pending_at"`
	LastEventTimestamp *time.Time       `json:"last_event_timestamp"`
	Records            []testWireRecord `json:"records"`
}

type testBatchResponse struct {
	Accepted  []uint64 `json:"accepted"`
	RequestID string   `json:"request_id"`
}

func TestOutageRecovery_OrderedLosslessDedup(t *testing.T) {
	var (
		mu       sync.Mutex
		received []testWireRecord
		offline  atomic.Bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var batch testBatchRequest
		if err := json.Unmarshal(body, &batch); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		accepted := make([]uint64, len(batch.Records))
		mu.Lock()
		for i, rec := range batch.Records {
			received = append(received, rec)
			accepted[i] = rec.Sequence
		}
		mu.Unlock()

		resp := testBatchResponse{
			Accepted:  accepted,
			RequestID: fmt.Sprintf("req-%d", time.Now().UnixNano()),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ctx := context.Background()

	store, err := sqlite.Open(ctx, sqlite.Config{
		Path:                   filepath.Join(t.TempDir(), "queue.db"),
		SiteID:                 "site-test-01",
		GatewayID:              "gw-test-01",
		MaxStorageBytes:        50 << 20,
		MaxEventBytes:          1 << 20,
		WarningPercent:         80,
		CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	client := uploader.NewIngestClient(server.URL+"/v1/ingest", "test-key", 5*time.Second)
	cfg := uploader.Config{
		GatewayID:     "gw-test-01",
		BatchSize:     50,
		BatchMaxBytes: 1 << 20,
		PollInterval:  50 * time.Millisecond,
		BaseDelay:     10 * time.Millisecond,
		MaxDelay:      50 * time.Millisecond,
		MaxRetries:    20,
	}

	now := time.Now().UTC()

	persistEvents := func(start, end int, eventTime time.Time) {
		t.Helper()
		for i := start; i <= end; i++ {
			seq, err := store.Persist(ctx, domain.Event{
				DeviceID:       fmt.Sprintf("dev-%02d", i),
				AssetType:      domain.AssetSmartMeter,
				MQTTTopic:      fmt.Sprintf("site/gw/smartmeter/dev-%02d/telemetry", i),
				Payload:        []byte(fmt.Sprintf(`{"reading":%d,"watts":%.1f}`, i, float64(i)*100.5)),
				Priority:       domain.PriorityNormal,
				EventTimestamp: eventTime,
				EdgeReceivedAt: now,
			})
			if err != nil {
				t.Fatalf("persist event %d: %v", i, err)
			}
			if err := store.MarkPendingUpload(ctx, seq); err != nil {
				t.Fatalf("mark pending upload %d: %v", i, err)
			}
		}
	}

	receivedCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(received)
	}

	waitForCount := func(target int, timeout time.Duration) {
		t.Helper()
		deadline := time.After(timeout)
		for {
			if receivedCount() >= target {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for %d records (got %d)", target, receivedCount())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	// Phase 1: Normal operation — persist events 1-10 with current timestamps.
	persistEvents(1, 10, now)

	uploaderCtx, cancelUploader := context.WithCancel(ctx)
	u := uploader.New(cfg, store, client, store)
	go u.Run(uploaderCtx)

	waitForCount(10, 5*time.Second)
	cancelUploader()
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	phase1Count := len(received)
	mu.Unlock()

	if phase1Count != 10 {
		t.Fatalf("phase 1: expected 10 records, got %d", phase1Count)
	}
	for i, rec := range received[:10] {
		expectedSeq := uint64(i + 1)
		if rec.Sequence != expectedSeq {
			t.Errorf("phase 1: record %d has sequence %d, want %d", i, rec.Sequence, expectedSeq)
		}
	}

	// Phase 2: Network failure — server returns 503, persist events 11-20.
	offline.Store(true)
	outageEventTime := now.Add(-3 * time.Minute)
	persistEvents(11, 20, outageEventTime)

	uploaderCtx2, cancelUploader2 := context.WithCancel(ctx)
	u2 := uploader.New(cfg, store, client, store)
	go u2.Run(uploaderCtx2)

	time.Sleep(300 * time.Millisecond)
	cancelUploader2()
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	phase2Count := len(received)
	mu.Unlock()

	if phase2Count != 10 {
		t.Fatalf("phase 2: expected still 10 records (no new accepted), got %d", phase2Count)
	}

	// Phase 3: Recovery — bring server back, drain the backlog.
	offline.Store(false)

	uploaderCtx3, cancelUploader3 := context.WithCancel(ctx)
	u3 := uploader.New(cfg, store, client, store)
	go u3.Run(uploaderCtx3)

	waitForCount(20, 10*time.Second)
	cancelUploader3()
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	allReceived := make([]testWireRecord, len(received))
	copy(allReceived, received)
	mu.Unlock()

	if len(allReceived) != 20 {
		t.Fatalf("phase 3: expected 20 records total, got %d", len(allReceived))
	}

	seqSeen := make(map[uint64]int, 20)
	for i, rec := range allReceived {
		seqSeen[rec.Sequence]++
		expectedSeq := uint64(i + 1)
		if rec.Sequence != expectedSeq {
			t.Errorf("ordering: position %d has sequence %d, want %d", i, rec.Sequence, expectedSeq)
		}
	}

	for seq := uint64(1); seq <= 20; seq++ {
		count := seqSeen[seq]
		if count != 1 {
			t.Errorf("sequence %d appeared %d times, want exactly 1", seq, count)
		}
	}

	// Phase 4: Replay flag verification.
	for _, rec := range allReceived[:10] {
		if rec.Replay {
			t.Errorf("sequence %d should have replay=false (uploaded promptly), got true", rec.Sequence)
		}
	}
	for _, rec := range allReceived[10:] {
		if !rec.Replay {
			t.Errorf("sequence %d should have replay=true (buffered during outage), got false", rec.Sequence)
		}
	}

	// Phase 5: Dedup — running the uploader on a drained queue produces no new records.
	uploaderCtx4, cancelUploader4 := context.WithCancel(ctx)
	u4 := uploader.New(cfg, store, client, store)
	go u4.Run(uploaderCtx4)

	time.Sleep(300 * time.Millisecond)
	cancelUploader4()
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	finalCount := len(received)
	mu.Unlock()

	if finalCount != 20 {
		t.Errorf("phase 5: expected 20 records (no duplicates from re-run), got %d", finalCount)
	}
}
