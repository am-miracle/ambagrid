package ingest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	produced []ingest.IngestRecord
	err      error
}

func (s *stubProducer) Produce(_ context.Context, _ string, rec ingest.IngestRecord) error {
	if s.err != nil {
		return s.err
	}
	s.produced = append(s.produced, rec)
	return nil
}

func newHandler(auth ingest.Authenticator, store ingest.DedupStore, producer ingest.Producer) *ingest.Handler {
	return ingest.NewHandler(ingest.HandlerConfig{
		Auth:     auth,
		Store:    store,
		Producer: producer,
		MaxBody:  1 << 20,
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
	batch := ingest.BatchRequest{Records: records}
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
	h := newHandler(
		&stubAuth{siteID: "site-01"},
		ingest.NewMemoryDedupStore(),
		producer,
	)

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
	if len(producer.produced) != 1 {
		t.Fatalf("expected 1 produced, got %d", len(producer.produced))
	}
}

func TestIngest_Unauthenticated(t *testing.T) {
	h := newHandler(
		&stubAuth{err: ingest.ErrUnauthorized},
		ingest.NewMemoryDedupStore(),
		&stubProducer{},
	)

	w := postBatch(h, []ingest.IngestRecord{validRecord(1, "site-01")})

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestIngest_DuplicateDetection(t *testing.T) {
	store := ingest.NewMemoryDedupStore()
	producer := &stubProducer{}
	h := newHandler(&stubAuth{siteID: "site-01"}, store, producer)

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
	if len(producer.produced) != 1 {
		t.Fatalf("duplicate should not produce again, got %d produces", len(producer.produced))
	}
}

func TestIngest_ValidationErrors(t *testing.T) {
	h := newHandler(
		&stubAuth{siteID: "site-01"},
		ingest.NewMemoryDedupStore(),
		&stubProducer{},
	)

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

func TestIngest_SiteMismatch(t *testing.T) {
	h := newHandler(
		&stubAuth{siteID: "site-01"},
		ingest.NewMemoryDedupStore(),
		&stubProducer{},
	)

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
	h := newHandler(
		&stubAuth{siteID: "site-01"},
		ingest.NewMemoryDedupStore(),
		producer,
	)

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

func TestIngest_EmptyBatch(t *testing.T) {
	h := newHandler(
		&stubAuth{siteID: "site-01"},
		ingest.NewMemoryDedupStore(),
		&stubProducer{},
	)

	w := postBatch(h, []ingest.IngestRecord{})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestIngest_MalformedJSON(t *testing.T) {
	h := newHandler(
		&stubAuth{siteID: "site-01"},
		ingest.NewMemoryDedupStore(),
		&stubProducer{},
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest", strings.NewReader("{invalid"))
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestIngest_ProduceFailureRollsBackDedup(t *testing.T) {
	store := ingest.NewMemoryDedupStore()
	producer := &stubProducer{err: errors.New("kafka down")}
	h := newHandler(&stubAuth{siteID: "site-01"}, store, producer)

	w := postBatch(h, []ingest.IngestRecord{validRecord(1, "site-01")})

	var resp ingest.BatchResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Rejected) != 1 {
		t.Fatalf("expected 1 rejected, got %d", len(resp.Rejected))
	}

	producer.err = nil
	w2 := postBatch(h, []ingest.IngestRecord{validRecord(1, "site-01")})
	var resp2 ingest.BatchResponse
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	if len(resp2.Accepted) != 1 {
		t.Fatalf("after rollback, retry should succeed; accepted=%v rejected=%v", resp2.Accepted, resp2.Rejected)
	}
}

func TestIngest_HealthEndpoint(t *testing.T) {
	h := newHandler(
		&stubAuth{siteID: "site-01"},
		ingest.NewMemoryDedupStore(),
		&stubProducer{},
	)

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
