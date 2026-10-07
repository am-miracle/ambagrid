package ingest

import "time"

type BatchRequest struct {
	GatewayID          string         `json:"gateway_id"`
	QueueDepth         int64          `json:"queue_depth"`
	OldestPendingAt    *time.Time     `json:"oldest_pending_at"`
	LastEventTimestamp *time.Time     `json:"last_event_timestamp"`
	Records            []IngestRecord `json:"records"`
}

type IngestRecord struct {
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

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}
