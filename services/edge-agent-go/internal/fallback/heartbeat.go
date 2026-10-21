package fallback

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Heartbeat is an SMS liveness signal sent only while packet data is
// unavailable. Its sequence is a dedicated counter, never the event sequence,
// so heartbeats leave no gaps in replay reconciliation.
type Heartbeat struct {
	SiteID    string
	GatewayID string
	Sequence  uint64
	SentAt    time.Time
}

func FormatHeartbeat(heartbeat Heartbeat) (string, error) {
	if invalidToken(heartbeat.SiteID) || invalidToken(heartbeat.GatewayID) {
		return "", errors.New("site and gateway IDs must be non-empty GSM-7 tokens")
	}
	if heartbeat.Sequence == 0 || heartbeat.SentAt.IsZero() {
		return "", errors.New("heartbeat sequence and send time are required")
	}
	message := strings.Join([]string{
		"AMBAGRID", "v=1", "kind=HB", "site=" + heartbeat.SiteID, "gw=" + heartbeat.GatewayID,
		"seq=" + strconv.FormatUint(heartbeat.Sequence, 10),
		"ts=" + strconv.FormatInt(heartbeat.SentAt.UTC().Unix(), 10),
	}, "|")
	if septets := gsmSeptets(message); septets > 160 {
		return "", fmt.Errorf("heartbeat message is %d GSM-7 septets; maximum is 160", septets)
	}
	return message, nil
}

type HeartbeatState struct {
	LastUploadSuccess *time.Time
	LastAttemptAt     *time.Time
	LastAttemptFailed bool
	AttemptsLastDay   int
}

type HeartbeatStore interface {
	HeartbeatState(ctx context.Context, now time.Time) (HeartbeatState, error)
	ReserveHeartbeat(ctx context.Context, attemptedAt time.Time) (uint64, error)
	RecordHeartbeat(ctx context.Context, sequence uint64, failureReason string) error
}

type HeartbeatConfig struct {
	SiteID    string
	GatewayID string
	Interval  time.Duration
	// DailyLimit is a separate budget from critical incidents, so heartbeats
	// can never exhaust the SMS allowance reserved for alerts.
	DailyLimit int
}

func (c *Coordinator) EnableHeartbeat(cfg HeartbeatConfig, store HeartbeatStore) {
	c.heartbeatCfg = cfg
	c.heartbeats = store
}

func (c *Coordinator) runHeartbeat(ctx context.Context, now time.Time) error {
	state, err := c.heartbeats.HeartbeatState(ctx, now)
	if err != nil {
		return err
	}
	if !c.heartbeatDue(state, now) {
		return nil
	}
	sequence, err := c.heartbeats.ReserveHeartbeat(ctx, now)
	if err != nil {
		return fmt.Errorf("reserve heartbeat: %w", err)
	}
	message, err := FormatHeartbeat(Heartbeat{
		SiteID: c.heartbeatCfg.SiteID, GatewayID: c.heartbeatCfg.GatewayID, Sequence: sequence, SentAt: now,
	})
	if err != nil {
		return errors.Join(err, c.heartbeats.RecordHeartbeat(ctx, sequence, err.Error()))
	}
	sendErr := c.sender.Send(ctx, c.cfg.Destination, message)
	failureReason := ""
	if sendErr != nil {
		failureReason = sendErr.Error()
		sendErr = fmt.Errorf("send heartbeat %d: %w", sequence, sendErr)
	}
	return errors.Join(sendErr, c.heartbeats.RecordHeartbeat(ctx, sequence, failureReason))
}

func (c *Coordinator) heartbeatDue(state HeartbeatState, now time.Time) bool {
	offlineSince := c.startedAt
	if state.LastUploadSuccess != nil {
		offlineSince = *state.LastUploadSuccess
	}
	if now.Sub(offlineSince) < c.cfg.OfflineAfter || state.AttemptsLastDay >= c.heartbeatCfg.DailyLimit {
		return false
	}
	// A successful HTTP upload closes the previous outage. Its heartbeat
	// attempts must not delay the first liveness signal for a later outage.
	if state.LastAttemptAt == nil ||
		(state.LastUploadSuccess != nil && !state.LastAttemptAt.After(*state.LastUploadSuccess)) {
		return true
	}
	wait := c.heartbeatCfg.Interval
	if state.LastAttemptFailed {
		wait = min(wait, c.cfg.RetryInterval)
	}
	return now.Sub(*state.LastAttemptAt) >= wait
}
