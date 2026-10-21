// client.go is the HTTP transport for uploading telemetry batches to the
// ingestion service. It maps domain.Record → wire JSON and sends the batch.
package uploader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"edge-agent-go/internal/domain"
)

type IngestClient struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

func NewIngestClient(endpoint, apiKey string, timeout time.Duration) *IngestClient {
	return &IngestClient{
		endpoint: endpoint,
		apiKey:   apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type batchRequest struct {
	GatewayID            string       `json:"gateway_id"`
	QueueDepth           int64        `json:"queue_depth"`
	OldestPendingAt      *time.Time   `json:"oldest_pending_at"`
	LastEventTimestamp   *time.Time   `json:"last_event_timestamp"`
	LastSuccessfulSyncAt *time.Time   `json:"last_successful_sync_at"`
	LastFailedSyncReason string       `json:"last_failed_sync_reason,omitempty"`
	Records              []wireRecord `json:"records"`
}

type HealthReport struct {
	GatewayID            string
	QueueDepth           int64
	OldestPendingAt      *time.Time
	LastEventTimestamp   *time.Time
	LastSuccessfulSyncAt *time.Time
	LastFailedSyncReason string
}

type wireRecord struct {
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

type BatchResponse struct {
	Accepted  []uint64      `json:"accepted"`
	Rejected  []RecordError `json:"rejected,omitempty"`
	RequestID string        `json:"request_id"`
}

type RecordError struct {
	Index    int    `json:"index"`
	Sequence uint64 `json:"sequence"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type UploadResult struct {
	Response   *BatchResponse
	StatusCode int
}

func (c *IngestClient) Upload(ctx context.Context, records []domain.Record, uploadedAt time.Time, health HealthReport) (*UploadResult, error) {
	wire := make([]wireRecord, len(records))
	for i, r := range records {
		wire[i] = wireRecord{
			Sequence:       r.Sequence,
			SiteID:         r.SiteID,
			GatewayID:      r.GatewayID,
			DeviceID:       r.DeviceID,
			AssetType:      string(r.AssetType),
			MQTTTopic:      r.MQTTTopic,
			Payload:        r.Payload,
			Priority:       int(r.Priority),
			EventTimestamp: r.EventTimestamp,
			EdgeReceivedAt: r.EdgeReceivedAt,
			UploadedAt:     uploadedAt,
			Replay:         r.ReplayRequired,
			CriticalCode:   r.CriticalCode,
			CriticalValue:  r.CriticalValue,
		}
	}

	body, err := json.Marshal(batchRequest{
		GatewayID:            health.GatewayID,
		QueueDepth:           health.QueueDepth,
		OldestPendingAt:      health.OldestPendingAt,
		LastEventTimestamp:   health.LastEventTimestamp,
		LastSuccessfulSyncAt: health.LastSuccessfulSyncAt,
		LastFailedSyncReason: health.LastFailedSyncReason,
		Records:              wire,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal upload batch: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &UploadResult{}, fmt.Errorf("send upload request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &UploadResult{StatusCode: resp.StatusCode}, fmt.Errorf("read upload response: %w", err)
	}

	// Preserve 401 as a distinct operational error: credentials need intervention,
	// while the uploader keeps the affected telemetry durable for later recovery.
	if resp.StatusCode == http.StatusUnauthorized {
		return &UploadResult{StatusCode: resp.StatusCode},
			fmt.Errorf("authentication failed (HTTP %d)", resp.StatusCode)
	}

	if resp.StatusCode >= 500 {
		return &UploadResult{StatusCode: resp.StatusCode},
			fmt.Errorf("server error (HTTP %d): %s", resp.StatusCode, truncate(string(respBody), 256))
	}

	var batchResp BatchResponse
	if err := json.Unmarshal(respBody, &batchResp); err != nil {
		return &UploadResult{StatusCode: resp.StatusCode},
			fmt.Errorf("decode upload response: %w", err)
	}

	return &UploadResult{
		Response:   &batchResp,
		StatusCode: resp.StatusCode,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
