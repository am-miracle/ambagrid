package fallback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	MaxIncidentAttempts   = 3
	IncidentAttemptWindow = 15 * time.Minute
)

func MinimumRetryInterval(maxAttempts int) time.Duration {
	if maxAttempts <= 1 {
		return 0
	}
	return IncidentAttemptWindow / time.Duration(MaxIncidentAttempts-1)
}

type Candidate struct {
	Event
	OpenedAt          time.Time
	UploadFailures    int
	LastUploadSuccess *time.Time
	Attempts          int
	AttemptsLastHour  int
	AttemptsLastDay   int
	NextAttemptAt     *time.Time
}

type AttemptResult struct {
	EventKey      EventKey
	AttemptedAt   time.Time
	Succeeded     bool
	FailureReason string
	NextAttemptAt *time.Time
}

type Store interface {
	FallbackCandidates(ctx context.Context, now time.Time) ([]Candidate, error)
	ReserveFallbackAttempt(ctx context.Context, eventKey EventKey, attemptedAt time.Time, retryAfter time.Duration) error
	RecordFallbackAttempt(ctx context.Context, result AttemptResult) error
}

type Sender interface {
	Send(ctx context.Context, destination, message string) error
}

type Config struct {
	Destination      string
	PollInterval     time.Duration
	FailureThreshold int
	OfflineAfter     time.Duration
	MaxAttempts      int
	HourlyLimit      int
	DailyLimit       int
	RetryInterval    time.Duration
}

type Coordinator struct {
	cfg          Config
	store        Store
	sender       Sender
	now          func() time.Time
	startedAt    time.Time
	heartbeats   HeartbeatStore
	heartbeatCfg HeartbeatConfig
}

func NewCoordinator(cfg Config, store Store, sender Sender) *Coordinator {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.MaxAttempts <= 0 || cfg.MaxAttempts > MaxIncidentAttempts {
		cfg.MaxAttempts = MaxIncidentAttempts
	}
	if minimum := MinimumRetryInterval(cfg.MaxAttempts); cfg.RetryInterval < minimum {
		cfg.RetryInterval = minimum
	}
	return &Coordinator{cfg: cfg, store: store, sender: sender, now: time.Now}
}

func (c *Coordinator) SetClock(now func() time.Time) {
	c.now = now
}

func (c *Coordinator) Run(ctx context.Context) {
	if err := c.RunOnce(ctx); err != nil {
		slog.Error("run SMS fallback", "error", err)
	}
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.RunOnce(ctx); err != nil {
				slog.Error("run SMS fallback", "error", err)
			}
		}
	}
}

func (c *Coordinator) RunOnce(ctx context.Context) error {
	now := c.now().UTC()
	if c.startedAt.IsZero() {
		c.startedAt = now
	}
	candidates, err := c.store.FallbackCandidates(ctx, now)
	if err != nil {
		return err
	}
	var resultErr error
	hourAttempts, dayAttempts := 0, 0
	for _, candidate := range candidates {
		candidate.AttemptsLastHour += hourAttempts
		candidate.AttemptsLastDay += dayAttempts
		if !c.eligible(candidate, now) {
			continue
		}
		message, err := Format(candidate.Event)
		if err != nil {
			resultErr = errors.Join(resultErr, err)
			continue
		}
		if err := c.store.ReserveFallbackAttempt(ctx, candidate.EventKey(), now, c.cfg.RetryInterval); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("reserve %s: %w", candidate.EventKey(), err))
			continue
		}
		hourAttempts++
		dayAttempts++
		sendErr := c.sender.Send(ctx, c.cfg.Destination, message)
		result := AttemptResult{
			EventKey: candidate.EventKey(), AttemptedAt: now, Succeeded: sendErr == nil,
		}
		if sendErr != nil {
			result.FailureReason = sendErr.Error()
			next := now.Add(c.cfg.RetryInterval)
			result.NextAttemptAt = &next
		}
		if err := c.store.RecordFallbackAttempt(ctx, result); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
		if sendErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("send %s: %w", candidate.EventKey(), sendErr))
		}
	}
	// Runs after critical incidents so a heartbeat never delays an alert on the modem.
	if c.heartbeats != nil {
		resultErr = errors.Join(resultErr, c.runHeartbeat(ctx, now))
	}
	return resultErr
}

func (c *Coordinator) eligible(candidate Candidate, now time.Time) bool {
	if candidate.Attempts >= c.cfg.MaxAttempts ||
		candidate.AttemptsLastHour >= c.cfg.HourlyLimit ||
		candidate.AttemptsLastDay >= c.cfg.DailyLimit ||
		(candidate.NextAttemptAt != nil && candidate.NextAttemptAt.After(now)) {
		return false
	}
	if candidate.UploadFailures >= c.cfg.FailureThreshold {
		return true
	}
	reference := candidate.OpenedAt
	if candidate.LastUploadSuccess != nil {
		reference = *candidate.LastUploadSuccess
	}
	return now.Sub(reference) >= c.cfg.OfflineAfter
}
