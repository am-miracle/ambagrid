package ingest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ingestion-go/internal/ingest"
)

type stubAuth struct {
	siteID string
	err    error
}

func (s *stubAuth) Authenticate(_ *http.Request) (string, error) {
	return s.siteID, s.err
}

type stubProducer struct {
	mu       sync.Mutex
	produced []ingest.IngestRecord
	err      error
}

func (s *stubProducer) Produce(_ context.Context, _ string, rec ingest.IngestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.produced = append(s.produced, rec)
	return nil
}

func (s *stubProducer) producedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.produced)
}

func (s *stubProducer) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

type stubHealth struct {
	reports []ingest.SiteHealthReport
	err     error
}

func (s *stubHealth) RecordContact(_ context.Context, report ingest.SiteHealthReport) error {
	s.reports = append(s.reports, report)
	return s.err
}

func newHandler(auth ingest.Authenticator, producer *stubProducer) *ingest.Handler {
	return ingest.NewHandler(ingest.HandlerConfig{
		Auth:      auth,
		Publisher: ingest.NewOncePublisher(producer),
		Health:    &stubHealth{},
		MaxBody:   1 << 20,
	})
}

func validRecord(seq uint64, siteID string) ingest.IngestRecord {
	return ingest.IngestRecord{
		Sequence:       seq,
		SiteID:         siteID,
		GatewayID:      "gw-01",
		DeviceID:       "meter-001",
		AssetType:      "smart_meter",
		MQTTTopic:      "africa-west/" + siteID + "/smartmeter/meter-001/telemetry",
		Payload:        []byte(`{"device_id":"meter-001","device_type":"DEVICE_TYPE_SMART_METER","timestamp_utc":1700000000,"site_id":"` + siteID + `"}`),
		Priority:       0,
		EventTimestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EdgeReceivedAt: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
		UploadedAt:     time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC),
		Replay:         false,
	}
}

func postBatch(handler http.Handler, records []ingest.IngestRecord) *httptest.ResponseRecorder {
	var lastEvent *time.Time
	for _, record := range records {
		if lastEvent == nil || record.EventTimestamp.After(*lastEvent) {
			t := record.EventTimestamp
			lastEvent = &t
		}
	}
	batch := ingest.BatchRequest{
		GatewayID: "gw-01", LastEventTimestamp: lastEvent, Records: records,
	}
	body, _ := json.Marshal(batch)
	req := httptest.NewRequest(http.MethodPost, "/v1/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestIngest_Success(t *testing.T) {
	producer := &stubProducer{}
	h := newHandler(&stubAuth{siteID: "site-01"}, producer)

	rec := validRecord(1, "site-01")
	w := postBatch(h, []ingest.IngestRecord{rec})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ingest.BatchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Accepted) != 1 || resp.Accepted[0] != 1 {
		t.Fatalf("expected accepted=[1], got %v", resp.Accepted)
	}
	if len(resp.Rejected) != 0 {
		t.Fatalf("expected no rejected, got %v", resp.Rejected)
	}
	if producer.producedCount() != 1 {
		t.Fatalf("expected 1 produced, got %d", producer.producedCount())
	}
}

func TestIngest_Unauthenticated(t *testing.T) {
	h := newHandler(&stubAuth{err: ingest.ErrUnauthorized}, &stubProducer{})

	w := postBatch(h, []ingest.IngestRecord{validRecord(1, "site-01")})

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestIngest_DuplicateDetection(t *testing.T) {
	producer := &stubProducer{}
	h := newHandler(&stubAuth{siteID: "site-01"}, producer)

	w1 := postBatch(h, []ingest.IngestRecord{validRecord(1, "site-01")})
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", w1.Code)
	}

	w2 := postBatch(h, []ingest.IngestRecord{validRecord(1, "site-01")})
	if w2.Code != http.StatusOK {
		t.Fatalf("duplicate request: expected 200, got %d", w2.Code)
	}

	var resp ingest.BatchResponse
	json.Unmarshal(w2.Body.Bytes(), &resp)
	if len(resp.Accepted) != 1 || resp.Accepted[0] != 1 {
		t.Fatalf("duplicate should be accepted (idempotent), got accepted=%v", resp.Accepted)
	}
	if producer.producedCount() != 1 {
		t.Fatalf("duplicate should not produce again, got %d produces", producer.producedCount())
	}
}

func TestIngest_ValidationErrors(t *testing.T) {
	h := newHandler(&stubAuth{siteID: "site-01"}, &stubProducer{})

	bad := ingest.IngestRecord{
		Sequence:  0,
		SiteID:    "site-01",
		DeviceID:  "",
		MQTTTopic: "",
		Payload:   nil,
	}

	w := postBatch(h, []ingest.IngestRecord{bad})

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}

	var resp ingest.BatchResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Rejected) != 1 {
		t.Fatalf("expected 1 rejected, got %d", len(resp.Rejected))
	}
	if resp.Rejected[0].Code != "validation_failed" {
		t.Fatalf("expected validation_failed code, got %q", resp.Rejected[0].Code)
	}
}

func TestIngest_RejectsCriticalCodeForWrongAssetType(t *testing.T) {
	h := newHandler(&stubAuth{siteID: "site-01"}, &stubProducer{})

	rec := validRecord(1, "site-01")
	rec.CriticalCode = "INVERTER_FAILURE"
	w := postBatch(h, []ingest.IngestRecord{rec})

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIngest_SiteMismatch(t *testing.T) {
	h := newHandler(&stubAuth{siteID: "site-01"}, &stubProducer{})

	rec := validRecord(1, "site-99")
	w := postBatch(h, []ingest.IngestRecord{rec})

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}

	var resp ingest.BatchResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Rejected) != 1 || resp.Rejected[0].Code != "site_mismatch" {
		t.Fatalf("expected site_mismatch rejection, got %v", resp.Rejected)
	}
}

func TestIngest_PartialFailure(t *testing.T) {
	producer := &stubProducer{}
	h := newHandler(&stubAuth{siteID: "site-01"}, producer)

	good := validRecord(1, "site-01")
	bad := ingest.IngestRecord{Sequence: 0, SiteID: "site-01"}

	w := postBatch(h, []ingest.IngestRecord{good, bad})

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d: %s", w.Code, w.Body.String())
	}

	var resp ingest.BatchResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Accepted) != 1 {
		t.Fatalf("expected 1 accepted, got %d", len(resp.Accepted))
	}
	if len(resp.Rejected) != 1 {
		t.Fatalf("expected 1 rejected, got %d", len(resp.Rejected))
	}
}

func TestIngest_EmptyBatchIsAHeartbeat(t *testing.T) {
	health := &stubHealth{}
	h := ingest.NewHandler(ingest.HandlerConfig{
		Auth:      &stubAuth{siteID: "site-01"},
		Publisher: ingest.NewOncePublisher(&stubProducer{}),
		Health:    health,
		MaxBody:   1 << 20,
	})

	w := postBatch(h, []ingest.IngestRecord{})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(health.reports) != 1 || health.reports[0].SiteID != "site-01" || health.reports[0].GatewayID != "gw-01" {
		t.Fatalf("heartbeat report = %+v", health.reports)
	}
	if health.reports[0].ContactAt.IsZero() {
		t.Fatal("heartbeat did not record contact time")
	}
}

func TestIngest_MalformedJSON(t *testing.T) {
	h := newHandler(&stubAuth{siteID: "site-01"}, &stubProducer{})

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader("{invalid"))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestIngest_ProduceFailureAllowsRetry(t *testing.T) {
	producer := &stubProducer{err: errors.New("kafka down")}
	h := newHandler(&stubAuth{siteID: "site-01"}, producer)

	w := postBatch(h, []ingest.IngestRecord{validRecord(1, "site-01")})

	var resp ingest.BatchResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Rejected) != 1 {
		t.Fatalf("expected 1 rejected, got %d", len(resp.Rejected))
	}

	producer.setErr(nil)
	w2 := postBatch(h, []ingest.IngestRecord{validRecord(1, "site-01")})
	var resp2 ingest.BatchResponse
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	if len(resp2.Accepted) != 1 {
		t.Fatalf("after failure, retry should succeed; accepted=%v rejected=%v", resp2.Accepted, resp2.Rejected)
	}
}

func TestIngest_HealthEndpoint(t *testing.T) {
	h := newHandler(&stubAuth{siteID: "site-01"}, &stubProducer{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestAPIKeyAuth(t *testing.T) {
	auth := ingest.NewAPIKeyAuth([]ingest.APIKeyEntry{
		{Key: "key-abc", SiteID: "site-01"},
		{Key: "key-xyz", SiteID: "site-02"},
	})

	tests := []struct {
		name   string
		header string
		want   string
		err    bool
	}{
		{"valid key", "Bearer key-abc", "site-01", false},
		{"second key", "Bearer key-xyz", "site-02", false},
		{"wrong key", "Bearer wrong", "", true},
		{"missing header", "", "", true},
		{"not bearer", "Basic key-abc", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			got, err := auth.Authenticate(req)
			if tt.err && err == nil {
				t.Fatal("expected error")
			}
			if !tt.err && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected site %q, got %q", tt.want, got)
			}
		})
	}
}

func TestOncePublisher_ConcurrentSameSequence(t *testing.T) {
	var calls atomic.Int64
	gate := make(chan struct{})
	producer := &blockingProducer{gate: gate, calls: &calls}

	pub := ingest.NewOncePublisher(producer)
	rec := validRecord(1, "site-01")

	var wg sync.WaitGroup
	results := make([]publishResult, 2)

	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			dup, err := pub.PublishOnce(context.Background(), "site-01", rec)
			results[idx] = publishResult{dup: dup, err: err}
		}(i)
	}

	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 produce call, got %d", calls.Load())
	}

	dups := 0
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("unexpected error: %v", r.err)
		}
		if r.dup {
			dups++
		}
	}
	if dups != 1 {
		t.Fatalf("expected exactly 1 duplicate, got %d", dups)
	}
}

func TestOncePublisher_EvictsExpiredClaims(t *testing.T) {
	producer := &stubProducer{}
	pub := ingest.NewOncePublisher(producer,
		ingest.WithClaimTTL(50*time.Millisecond),
		ingest.WithSweepInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pub.RunEviction(ctx)

	rec := validRecord(1, "site-01")

	dup1, err := pub.PublishOnce(context.Background(), "site-01", rec)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if dup1 {
		t.Fatal("first publish should not be a duplicate")
	}

	dup2, err := pub.PublishOnce(context.Background(), "site-01", rec)
	if err != nil {
		t.Fatalf("immediate retry: %v", err)
	}
	if !dup2 {
		t.Fatal("immediate retry should be a duplicate")
	}

	time.Sleep(150 * time.Millisecond)

	dup3, err := pub.PublishOnce(context.Background(), "site-01", rec)
	if err != nil {
		t.Fatalf("post-eviction publish: %v", err)
	}
	if dup3 {
		t.Fatal("post-eviction publish should not be a duplicate")
	}
}

type publishResult struct {
	dup bool
	err error
}

type blockingProducer struct {
	gate  chan struct{}
	calls *atomic.Int64
}

func (p *blockingProducer) Produce(ctx context.Context, _ string, _ ingest.IngestRecord) error {
	p.calls.Add(1)
	select {
	case <-p.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
