package health_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/health"
	queuesqlite "edge-agent-go/internal/repository/sqlite"
)

func TestHealthAndMetricsExposeDurableQueueState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	q, err := queuesqlite.Open(ctx, queuesqlite.Config{
		Path:                   filepath.Join(t.TempDir(), "queue.db"),
		SiteID:                 "ng-kaji-01",
		GatewayID:              "gateway-01",
		MaxStorageBytes:        8 << 20,
		WarningPercent:         70,
		CriticalReservePercent: 10,
	})
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	defer q.Close()
	if _, err := q.Enqueue(ctx, domain.Event{
		DeviceID:       "met-0101",
		AssetType:      domain.AssetSmartMeter,
		MQTTTopic:      "africa-west/ng-kaji-01/smartmeter/met-0101/telemetry",
		Payload:        []byte(`{"voltage":231.4}`),
		EventTimestamp: time.Now().Add(-time.Hour),
		EdgeReceivedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("enqueue event: %v", err)
	}

	handler := health.NewHandler(q)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200; body: %s", health.Code, health.Body.String())
	}
	var body struct {
		State string `json:"state"`
		Depth int64  `json:"queue_depth"`
	}
	if err := json.Unmarshal(health.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.State != string(domain.QueueHealthy) || body.Depth != 1 {
		t.Fatalf("health response = state %q depth %d", body.State, body.Depth)
	}

	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", metrics.Code)
	}
	for _, want := range []string{
		"ambagrid_edge_queue_depth 1",
		"ambagrid_edge_queue_payload_bytes 17",
		`ambagrid_edge_storage_state{state="healthy"} 1`,
	} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Errorf("metrics body does not contain %q\n%s", want, metrics.Body.String())
		}
	}
}
