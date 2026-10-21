package domain

import (
	"strconv"
	"time"
)

type HeartbeatKey string

// GatewayHeartbeat is an SMS liveness observation sent while packet data is
// unavailable. It is not an alert and never feeds replay reconciliation.
type GatewayHeartbeat struct {
	SiteID    string
	GatewayID string
	Sequence  uint64
	SentAt    time.Time
}

func NewHeartbeatKey(siteID, gatewayID string, sequence uint64) HeartbeatKey {
	return HeartbeatKey("hb:" + siteID + ":" + gatewayID + ":" + strconv.FormatUint(sequence, 10))
}

func (h GatewayHeartbeat) Key() HeartbeatKey {
	return NewHeartbeatKey(h.SiteID, h.GatewayID, h.Sequence)
}

// ContactAt bounds liveness by both clocks: a gateway clock running ahead
// cannot make it look fresher than delivery, and store-and-forward delay
// cannot make a stale heartbeat look current.
func (h GatewayHeartbeat) ContactAt(receivedAt time.Time) time.Time {
	if h.SentAt.Before(receivedAt) {
		return h.SentAt
	}
	return receivedAt
}

type StoredHeartbeat struct {
	SentAt time.Time
}

func (s *StoredHeartbeat) Matches(h GatewayHeartbeat) bool {
	return s.SentAt.Equal(h.SentAt)
}

// InboundSMS carries exactly one of Critical or Heartbeat.
type InboundSMS struct {
	Critical  *CriticalFallbackEvent
	Heartbeat *GatewayHeartbeat
	Receipt   SMSReceipt
}
