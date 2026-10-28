package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxFailureReasonBytes = 500

var ErrInvalidReport = errors.New("invalid command report")

// EdgeCommand is a meter command as delivered to the site's edge agent.
// DeviceID is the meter's asset ID, the identity the gateway addresses it by.
type EdgeCommand struct {
	CommandID   string
	MeterID     string
	DeviceID    string
	CommandType MeterCommandType
	Status      MeterCommandStatus
	RequestedAt time.Time
	ExpiresAt   time.Time
	ExpiresIn   time.Duration
	CreditKWh   *float64
}

type CommandReport struct {
	CommandID     string
	Status        MeterCommandStatus
	OccurredAt    time.Time
	FailureReason string
}

type SiteCommand struct {
	SiteID  string
	Command MeterCommand
}

type ReconciliationReason string

const (
	ReconciliationOverturnedExpiry  ReconciliationReason = "overturned_expiry"
	ReconciliationConflictingReport ReconciliationReason = "conflicting_report"
	ReconciliationOutcomeUnknown    ReconciliationReason = "overdue_outcome_unknown"
)

// CommandReconciliation keeps an unresolved delivery separate from a conflicting report.
type CommandReconciliation struct {
	SiteCommand
	Site                     Site
	LastReportAt             *time.Time
	ConflictCount            int64
	LatestConflict           *CommandReport
	LatestConflictReceivedAt *time.Time
	// LegacyExpiredAt is when pre-fix platform code expired the command, which
	// may then have been delivered or even applied.
	LegacyExpiredAt *time.Time
	Age             time.Duration
	Overdue         time.Duration
}

// Reason prioritizes overturned legacy expiries because consumers already
// received meter.command.failed, ahead of retained conflicts and overdue commands.
func (r CommandReconciliation) Reason() ReconciliationReason {
	switch {
	case r.LegacyExpiredAt != nil && r.Command.Status != CommandStatusExpired:
		return ReconciliationOverturnedExpiry
	case r.ConflictCount > 0:
		return ReconciliationConflictingReport
	default:
		return ReconciliationOutcomeUnknown
	}
}

func (r CommandReport) Validate() error {
	if err := ValidateCommandID(r.CommandID); err != nil {
		return err
	}
	switch r.Status {
	case CommandStatusSent, CommandStatusAcknowledged, CommandStatusExpired:
		if r.FailureReason != "" {
			return fmt.Errorf("%w: failure_reason is only allowed for failed commands", ErrInvalidReport)
		}
	case CommandStatusFailed:
		if strings.TrimSpace(r.FailureReason) == "" {
			return fmt.Errorf("%w: failed commands need a failure_reason", ErrInvalidReport)
		}
		if len(r.FailureReason) > maxFailureReasonBytes {
			return fmt.Errorf("%w: failure_reason must not exceed %d bytes", ErrInvalidReport, maxFailureReasonBytes)
		}
	default:
		return fmt.Errorf("%w: status %q cannot be reported by an edge agent", ErrInvalidReport, r.Status)
	}
	if r.OccurredAt.IsZero() {
		return fmt.Errorf("%w: occurred_at is required", ErrInvalidReport)
	}
	return nil
}

// ClampOccurredAt bounds a gateway-reported time to when the command could
// have happened: not before it was requested, not after the platform's now.
func (c MeterCommand) ClampOccurredAt(at, now time.Time) time.Time {
	if at.Before(c.RequestedAt) {
		return c.RequestedAt
	}
	if at.After(now) {
		return now
	}
	return at
}

// ApplyReport ignores repeated or trailing reports and rejects conflicting outcomes.
// The service retains conflicting evidence separately, including reports after legacy expiry.
func (c MeterCommand) ApplyReport(r CommandReport) (MeterCommand, bool, error) {
	at := r.OccurredAt.UTC()
	conflict := func() (MeterCommand, bool, error) {
		return c, false, fmt.Errorf("%w: command %s is %s and cannot become %s", ErrConflict, c.CommandID, c.Status, r.Status)
	}
	switch c.Status {
	case CommandStatusAcknowledged:
		switch r.Status {
		case CommandStatusAcknowledged, CommandStatusSent:
			return c, false, nil
		}
		return conflict()
	case CommandStatusFailed:
		switch r.Status {
		case CommandStatusFailed, CommandStatusSent:
			return c, false, nil
		}
		return conflict()
	case CommandStatusSent:
		if r.Status == CommandStatusSent {
			return c, false, nil
		}
	case CommandStatusExpired:
		if r.Status == CommandStatusExpired {
			return c, false, nil
		}
		return conflict()
	case CommandStatusRequested, CommandStatusQueued:
	default:
		return conflict()
	}

	next := c
	next.Status = r.Status
	switch r.Status {
	case CommandStatusSent:
		next.SentAt = &at
	case CommandStatusAcknowledged:
		if next.SentAt == nil {
			next.SentAt = &at
		}
		// The schema requires acknowledged_at >= sent_at; both come from the
		// gateway clock, so only a reordered report could invert them.
		acknowledged := at
		if acknowledged.Before(*next.SentAt) {
			acknowledged = *next.SentAt
		}
		next.AcknowledgedAt = &acknowledged
	case CommandStatusFailed:
		reason := r.FailureReason
		next.FailureReason = &reason
	}
	return next, true, nil
}
