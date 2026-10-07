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
}

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
	AttemptCount   int
	NextAttemptAt  *time.Time
	LastError      string
}
