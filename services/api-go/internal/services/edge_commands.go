package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
	"api-go/internal/repository"
)

// ErrReportNeedsReconciliation is returned only after conflicting evidence commits.
var ErrReportNeedsReconciliation = fmt.Errorf("%w: command report retained for reconciliation", domain.ErrConflict)

const (
	defaultEdgeCommandBatch = 50
	maxEdgeCommandBatch     = 200
	commandExpiryActor      = "system:command-expiry"
)

// EdgeCommandService hands meter commands to a site's edge agent and records
// what the agent reports back. Every status change writes an audit event in
// the same transaction.
type EdgeCommandService struct {
	repository repository.EdgeCommandRepository
	topics     OutcomeTopics
	now        func() time.Time
	thresholds SiteHealthThresholds
	limits     PageLimits
}

// OutcomeTopics are the deployment topics for settled command outcomes.
type OutcomeTopics struct {
	Acknowledged string
	Failed       string
}

func NewEdgeCommandService(repository repository.EdgeCommandRepository, topics OutcomeTopics, thresholds SiteHealthThresholds, limits PageLimits) *EdgeCommandService {
	if topics.Acknowledged == "" {
		topics.Acknowledged = "meter.command.acknowledged"
	}
	if topics.Failed == "" {
		topics.Failed = "meter.command.failed"
	}
	return &EdgeCommandService{repository: repository, topics: topics, now: time.Now, thresholds: thresholds, limits: limits}
}

// Poll expires the site's overdue undelivered commands, then returns its
// pending ones with requested/queued ahead of sent, oldest first within each
// group, marking newly delivered ones queued.
func (s *EdgeCommandService) Poll(ctx context.Context, siteID string, limit int) ([]domain.EdgeCommand, error) {
	if limit <= 0 {
		limit = defaultEdgeCommandBatch
	}
	limit = min(limit, maxEdgeCommandBatch)

	var commands []domain.EdgeCommand
	err := s.repository.RunEdgeCommandTx(ctx, func(tx repository.EdgeCommandTx) error {
		now := s.now().UTC()
		expired, err := tx.ExpireOverdueCommands(ctx, siteID, now)
		if err != nil {
			return err
		}
		for _, cmd := range expired {
			if err := s.recordExpiry(ctx, tx, siteID, cmd, now); err != nil {
				return err
			}
		}

		commands, err = tx.ListPendingEdgeCommands(ctx, siteID, now, limit)
		if err != nil {
			return err
		}
		var delivered []string
		for i := range commands {
			commands[i].ExpiresIn = commands[i].ExpiresAt.Sub(now)
			if commands[i].Status == domain.CommandStatusRequested {
				delivered = append(delivered, commands[i].CommandID)
				commands[i].Status = domain.CommandStatusQueued
			}
		}
		return tx.MarkCommandsQueued(ctx, delivered)
	})
	if err != nil {
		return nil, err
	}
	return commands, nil
}

// Report applies one edge report. changed is false when the report was
// already recorded, which makes edge retries safe.
func (s *EdgeCommandService) Report(ctx context.Context, siteID string, report domain.CommandReport) (domain.MeterCommand, bool, error) {
	if err := report.Validate(); err != nil {
		if errors.Is(err, domain.ErrInvalidReport) {
			return domain.MeterCommand{}, false, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		return domain.MeterCommand{}, false, err
	}

	var (
		result      domain.MeterCommand
		changed     bool
		conflicting bool
	)
	err := s.repository.RunEdgeCommandTx(ctx, func(tx repository.EdgeCommandTx) error {
		current, err := tx.LockSiteCommand(ctx, siteID, report.CommandID)
		if err != nil {
			return err
		}
		original := report
		report.OccurredAt = current.ClampOccurredAt(report.OccurredAt, s.now().UTC())
		result, changed, err = current.ApplyReport(report)
		if errors.Is(err, domain.ErrConflict) {
			result, conflicting = current, true
			return tx.RetainConflictingReport(ctx, siteID, current, original)
		}
		if err != nil || !changed {
			return err
		}
		if err := tx.SaveCommandStatus(ctx, result); err != nil {
			return err
		}
		metadata := map[string]any{"occurred_at": report.OccurredAt.UTC(), "previous_status": current.Status}
		if report.FailureReason != "" {
			metadata["failure_reason"] = report.FailureReason
		}
		reporter := "edge:" + siteID
		if err := tx.InsertAuditEvent(ctx, siteID, reporter, "meter_command."+string(report.Status), "meter_command", report.CommandID, metadata); err != nil {
			return err
		}
		return s.publishOutcome(ctx, tx, result, siteID, reporter, report.OccurredAt)
	})
	if err != nil {
		return domain.MeterCommand{}, false, err
	}
	if conflicting {
		return result, false, ErrReportNeedsReconciliation
	}
	return result, changed, nil
}

func (s *EdgeCommandService) recordExpiry(ctx context.Context, tx repository.EdgeCommandTx, siteID string, cmd domain.MeterCommand, now time.Time) error {
	// Reconciliation treats an expiry audit without previous_status as written by
	// pre-fix code, which also expired queued commands.
	metadata := map[string]any{"expires_at": cmd.ExpiresAt.UTC(), "previous_status": domain.CommandStatusRequested}
	if err := tx.InsertAuditEvent(ctx, siteID, commandExpiryActor, "meter_command.expired", "meter_command", cmd.CommandID, metadata); err != nil {
		return err
	}
	return s.publishOutcome(ctx, tx, cmd, siteID, commandExpiryActor, now)
}

// ListReconciliation lists every site's commands when siteID is nil.
func (s *EdgeCommandService) ListReconciliation(ctx context.Context, siteID *string, limit int, cursor string) (page.Page[domain.CommandReconciliation], error) {
	limit, err := s.limits.Resolve(limit)
	if err != nil {
		return page.Page[domain.CommandReconciliation]{}, err
	}
	after, err := page.DecodeTimeIDCursor(cursor, domain.ValidateCommandID)
	if err != nil {
		return page.Page[domain.CommandReconciliation]{}, err
	}
	now := s.now().UTC()
	result, err := s.repository.ListCommandReconciliation(ctx, siteID, now, limit, after)
	if err != nil {
		return page.Page[domain.CommandReconciliation]{}, err
	}
	for i := range result.Items {
		item := &result.Items[i]
		item.Age = max(0, now.Sub(item.Command.RequestedAt))
		item.Overdue = max(0, now.Sub(item.Command.ExpiresAt))
		item.Site.HealthStatus = siteHealthStatus(item.Site, now, s.thresholds)
	}
	return result, nil
}

// SweepExpired settles a bounded batch that no edge has ever been offered.
func (s *EdgeCommandService) SweepExpired(ctx context.Context) (int, error) {
	count := 0
	err := s.repository.RunEdgeCommandTx(ctx, func(tx repository.EdgeCommandTx) error {
		now := s.now().UTC()
		commands, err := tx.ExpireUndeliveredCommands(ctx, now, maxEdgeCommandBatch)
		if err != nil {
			return err
		}
		for _, item := range commands {
			if err := s.recordExpiry(ctx, tx, item.SiteID, item.Command, now); err != nil {
				return err
			}
		}
		count = len(commands)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// RunExpiryWorker also expires commands at sites that never poll again.
func (s *EdgeCommandService) RunExpiryWorker(ctx context.Context, onError func(error)) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.SweepExpired(ctx); err != nil && ctx.Err() == nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// publishOutcome announces settled outcomes only; "sent" is an intermediate
// state, and a command whose outcome is unknown stays there.
func (s *EdgeCommandService) publishOutcome(ctx context.Context, tx repository.EdgeCommandTx, cmd domain.MeterCommand, siteID, reportedBy string, occurredAt time.Time) error {
	switch cmd.Status {
	case domain.CommandStatusAcknowledged:
		return tx.PublishCommandOutcome(ctx, s.topics.Acknowledged, "meter.command.acknowledged", cmd, siteID, reportedBy, occurredAt)
	case domain.CommandStatusFailed, domain.CommandStatusExpired:
		return tx.PublishCommandOutcome(ctx, s.topics.Failed, "meter.command.failed", cmd, siteID, reportedBy, occurredAt)
	}
	return nil
}
