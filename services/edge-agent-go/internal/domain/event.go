package domain

import "time"

type Priority int

const (
	PriorityNormal Priority = iota
	PriorityCritical
)

func (p Priority) Valid() bool {
	return p == PriorityNormal || p == PriorityCritical
}

type Event struct {
	DeviceID       string
	AssetType      AssetType
	MQTTTopic      string
	Payload        []byte
	Priority       Priority
	EventTimestamp time.Time
	EdgeReceivedAt time.Time
	CriticalCode   string
	CriticalValue  *float64
}

// Record is a persisted Event enriched with queue lifecycle metadata.
// Three timestamps trace the full latency chain:
//   - EventTimestamp: when the physical measurement was taken
//   - EdgeReceivedAt: when the edge agent ingested the reading
//   - UploadedAt:     when the record was sent to the ingestion service
type Record struct {
	Sequence       uint64
	SiteID         string
	GatewayID      string
	DeviceID       string
	AssetType      AssetType
	MQTTTopic      string
	Payload        []byte
	Priority       Priority
	Status         EventStatus
	EventTimestamp time.Time
	EdgeReceivedAt time.Time
	PersistedAt    time.Time
	UploadedAt     *time.Time
	AttemptCount   int
	NextAttemptAt  *time.Time
	LastError      string
	CriticalCode   string
	CriticalValue  *float64
}
