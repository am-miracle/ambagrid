package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
)

// The command row is locked by Report, serializing duplicate evidence checks.
func (t edgeCommandTx) RetainConflictingReport(ctx context.Context, siteID string, current domain.MeterCommand, report domain.CommandReport) error {
	fields := map[string]any{
		"platform_status": current.Status, "reported_status": report.Status,
		"occurred_at": report.OccurredAt.UTC(), "failure_reason": report.FailureReason,
	}
	// Preserve deduplication against conflicts stored before observations existed.
	if report.Observation != nil {
		fields["observation"] = report.Observation
	}
	metadata, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encode conflicting command report: %w", err)
	}
	tag, err := t.tx.Exec(ctx, `
		INSERT INTO audit_events(site_id, actor_id, action, subject_type, subject_id, metadata, occurred_at)
		SELECT $1, $2, 'meter_command.report_conflict', 'meter_command', $3, $4::jsonb, clock_timestamp()
		WHERE NOT EXISTS (
		    SELECT 1 FROM audit_events WHERE site_id = $1 AND subject_type = 'meter_command'
		    AND subject_id = $3 AND action = 'meter_command.report_conflict' AND metadata = $4::jsonb
		)`, siteID, domain.EdgeActor(siteID), report.CommandID, string(metadata))
	if err != nil {
		return fmt.Errorf("retain conflicting command report: %w", err)
	}
	if tag.RowsAffected() > 0 {
		_, err = t.tx.Exec(ctx, `UPDATE command_reconciliation_cases SET needs_review=true WHERE command_id=$1::uuid AND state='resolved'`, report.CommandID)
		if err != nil {
			return fmt.Errorf("flag case for review: %w", err)
		}
	}
	return nil
}

func meterCommandDests(c *domain.MeterCommand) []any {
	return []any{&c.CommandID, &c.MeterID, &c.CommandType, &c.Status, &c.RequestedBy,
		&c.Reason, &c.RequestedAt, &c.ExpiresAt, &c.SentAt, &c.AcknowledgedAt,
		&c.FailureReason, &c.CreditKWh, &c.CreditID}
}

func (t edgeCommandTx) ExpireUndeliveredCommands(ctx context.Context, now time.Time, limit int) ([]domain.SiteCommand, error) {
	rows, err := t.tx.Query(ctx, `
		WITH overdue AS (
		    SELECT mc.command_id, sm.site_id FROM meter_commands mc
		    JOIN smart_meters sm ON sm.meter_id = mc.meter_id
		    WHERE mc.status = 'requested' AND mc.expires_at <= $1
		    ORDER BY mc.expires_at, mc.command_id LIMIT $2
		    FOR UPDATE OF mc SKIP LOCKED
		)
		UPDATE meter_commands mc SET status = 'expired' FROM overdue
		WHERE mc.command_id = overdue.command_id
		RETURNING `+meterCommandColumns+`, overdue.site_id`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("expire undelivered commands: %w", err)
	}
	defer rows.Close()
	var commands []domain.SiteCommand
	for rows.Next() {
		var item domain.SiteCommand
		if err := rows.Scan(append(meterCommandDests(&item.Command), &item.SiteID)...); err != nil {
			return nil, fmt.Errorf("read expired command: %w", err)
		}
		commands = append(commands, item)
	}
	return commands, rows.Err()
}

// Each candidate branch reads a small partial index, so the per-command
// lookups below run only for commands that are reconciliation work.
const reconciliationCandidatesSQL = `
	WITH candidates AS (
     SELECT mc.command_id::text FROM command_reconciliation_cases rc
     JOIN meter_commands mc USING(command_id)
     JOIN smart_meters sm ON sm.meter_id=mc.meter_id
     WHERE (rc.state='investigating' OR rc.needs_review) %[1]s
     UNION
	    SELECT mc.command_id::text AS command_id FROM meter_commands mc
	    JOIN smart_meters sm ON sm.meter_id = mc.meter_id
	    WHERE mc.status IN ('queued', 'sent') AND mc.expires_at <= $1 %[1]s
	    UNION
	    SELECT mc.command_id::text FROM meter_commands mc
	    JOIN smart_meters sm ON sm.meter_id = mc.meter_id
	    WHERE ` + undeliveredCreditPredicate + ` %[1]s
	    UNION
	    SELECT a.subject_id FROM audit_events a
	    WHERE a.subject_type = 'meter_command' AND a.action = 'meter_command.report_conflict' %[2]s
	    UNION
	    SELECT a.subject_id FROM audit_events a
	    WHERE ` + legacyExpiryPredicate + ` %[2]s
	)
`

const reconciliationOpenCasesSQL = `
	FROM candidates c
	JOIN meter_commands mc ON mc.command_id = c.command_id::uuid
	JOIN smart_meters sm ON sm.meter_id = mc.meter_id
	LEFT JOIN command_reconciliation_cases rc ON rc.command_id=mc.command_id
 LEFT JOIN site_health h ON h.site_id = sm.site_id
	LEFT JOIN LATERAL (
	    SELECT contact_at, expires_at FROM sms_heartbeats WHERE site_id = sm.site_id
	    ORDER BY contact_at DESC LIMIT 1
	) hb ON true
	LEFT JOIN LATERAL (
	    SELECT max(occurred_at) AS last_report_at FROM audit_events a
	    WHERE a.site_id = sm.site_id AND a.subject_type = 'meter_command'
	      AND a.subject_id = c.command_id AND a.actor_id = 'edge:' || sm.site_id
	) reports ON true
	LEFT JOIN LATERAL (
	    SELECT count(*) AS total FROM audit_events a
	    WHERE a.site_id = sm.site_id AND a.subject_type = 'meter_command'
	      AND a.subject_id = c.command_id AND a.action = 'meter_command.report_conflict'
	) conflicts ON true
	LEFT JOIN LATERAL (
	    SELECT metadata, occurred_at FROM audit_events a
	    WHERE a.site_id = sm.site_id AND a.subject_type = 'meter_command'
	      AND a.subject_id = c.command_id AND a.action = 'meter_command.report_conflict'
	    ORDER BY occurred_at DESC, audit_event_id DESC LIMIT 1
	) latest ON true
	LEFT JOIN LATERAL (
	    SELECT min(a.occurred_at) AS expired_at FROM audit_events a
	    WHERE a.site_id = sm.site_id AND a.subject_id = c.command_id AND ` + legacyExpiryPredicate + `
	) legacy ON true
	WHERE ((mc.status IN ('queued', 'sent') AND mc.expires_at <= $1)
	       OR conflicts.total > 0
	       OR ` + undeliveredCreditPredicate + `
	       OR (legacy.expired_at IS NOT NULL AND mc.status <> 'expired')
 OR rc.state='investigating' OR rc.needs_review)
	  AND (rc.state IS DISTINCT FROM 'resolved' OR rc.needs_review)
`

const commandReconciliationSQL = reconciliationCandidatesSQL + `
	SELECT ` + meterCommandColumns + `, sm.site_id, h.last_contact_at,
	       h.last_event_timestamp, coalesce(h.queue_growing, false), hb.contact_at, hb.expires_at,
	       reports.last_report_at, conflicts.total,
	       latest.metadata, latest.occurred_at, legacy.expired_at, to_jsonb(rc), coalesce(rc.needs_review,false)
` + reconciliationOpenCasesSQL + `
	  AND ($2::timestamptz IS NULL OR (mc.requested_at, mc.command_id) > ($2, $3::uuid))
	ORDER BY mc.requested_at, mc.command_id LIMIT $4`

const reconciliationBacklogSQL = reconciliationCandidatesSQL + `
	SELECT count(*), min(mc.requested_at) ` + reconciliationOpenCasesSQL

// Matches meter_commands_undelivered_credit_idx. The balance still holds these
// credits and the unique credit_id index blocks a replacement command.
const undeliveredCreditPredicate = `mc.command_type = 'credit_meter' AND mc.status IN ('failed', 'expired')`

// Matches audit_events_legacy_command_expiry_idx. Every expiry since the fix
// records previous_status; earlier ones may have expired a delivered command.
// uncertainDeliveryAuditPredicate matches an audit row saying a device may have
// acted on a command: a reported send or acknowledgement, or a legacy expiry.
const uncertainDeliveryAuditPredicate = `a.subject_type = 'meter_command' AND ((a.action = 'meter_command.report_conflict'
	      AND a.metadata->>'reported_status' IN ('acknowledged', 'sent')) OR (` + legacyExpiryPredicate + `))`

const legacyExpiryPredicate = `a.subject_type = 'meter_command' AND a.action = 'meter_command.expired'
	      AND a.actor_id = '` + domain.CommandExpiryActor + `' AND NOT (a.metadata ? 'previous_status')`

func (s *EdgeCommandStore) ListCommandReconciliation(ctx context.Context, siteID *string, now time.Time, limit int, after *page.TimeIDCursor) (page.Page[domain.CommandReconciliation], error) {
	if s.queryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.queryTimeout)
		defer cancel()
	}
	var afterTime, afterID any
	if after != nil {
		afterTime, afterID = after.Time, after.ID
	}
	args := []any{now, afterTime, afterID, limit + 1}
	var commandSite, auditSite string
	if siteID != nil {
		args = append(args, *siteID)
		commandSite, auditSite = "AND sm.site_id = $5", "AND a.site_id = $5"
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(commandReconciliationSQL, commandSite, auditSite), args...)
	if err != nil {
		return page.Page[domain.CommandReconciliation]{}, fmt.Errorf("list command reconciliation: %w", err)
	}
	defer rows.Close()
	var items []domain.CommandReconciliation
	for rows.Next() {
		var item domain.CommandReconciliation
		var metadata, caseJSON []byte
		c := &item.Command
		if err := rows.Scan(append(meterCommandDests(c), &item.SiteID, &item.Site.LastContactAt,
			&item.Site.LastEventAt, &item.Site.QueueGrowing, &item.Site.LastSMSHeartbeatAt, &item.Site.SMSHeartbeatExpiresAt,
			&item.LastReportAt, &item.ConflictCount, &metadata, &item.LatestConflictReceivedAt, &item.LegacyExpiredAt, &caseJSON, &item.NeedsReview)...); err != nil {
			return page.Page[domain.CommandReconciliation]{}, fmt.Errorf("read command reconciliation: %w", err)
		}
		if caseJSON != nil {
			if err := json.Unmarshal(caseJSON, &item.Case); err != nil {
				return page.Page[domain.CommandReconciliation]{}, fmt.Errorf("decode reconciliation case: %w", err)
			}
		}
		if metadata != nil {
			evidence, err := decodeConflictMetadata(metadata)
			if err != nil {
				return page.Page[domain.CommandReconciliation]{}, err
			}
			item.LatestConflict = &domain.CommandReport{Observation: evidence.Observation, CommandID: c.CommandID, Status: evidence.Status, OccurredAt: evidence.OccurredAt, FailureReason: evidence.FailureReason}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return page.Page[domain.CommandReconciliation]{}, fmt.Errorf("read command reconciliation: %w", err)
	}
	return page.Build(items, limit, func(item domain.CommandReconciliation) string {
		return (page.TimeIDCursor{Time: item.Command.RequestedAt, ID: item.Command.CommandID}).Encode()
	}), nil
}

const unvendedBacklogSQL = `SELECT count(*), min(ec.created_at) FROM energy_credits ec JOIN payments p USING(payment_id)
	WHERE ec.delivery_mode='sts' AND p.status='confirmed'
	AND NOT EXISTS(SELECT 1 FROM sts_vends sv WHERE sv.payment_id=ec.payment_id)
	AND NOT EXISTS(SELECT 1 FROM sts_credit_refunds r WHERE r.credit_id=ec.credit_id)`

func (s *EdgeCommandStore) ReconciliationBacklog(ctx context.Context, now time.Time) (domain.ReconciliationBacklog, error) {
	if s.queryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.queryTimeout)
		defer cancel()
	}
	var backlog domain.ReconciliationBacklog
	var oldestCase, oldestUnvended *time.Time
	if err := s.pool.QueryRow(ctx, fmt.Sprintf(reconciliationBacklogSQL, "", ""), now).Scan(&backlog.OpenCases, &oldestCase); err != nil {
		return backlog, fmt.Errorf("count open reconciliation cases: %w", err)
	}
	if err := s.pool.QueryRow(ctx, unvendedBacklogSQL).Scan(&backlog.UnvendedCredits, &oldestUnvended); err != nil {
		return backlog, fmt.Errorf("count unvended STS credits: %w", err)
	}
	if oldestCase != nil {
		backlog.OldestOpenCase = max(0, now.Sub(*oldestCase))
	}
	if oldestUnvended != nil {
		backlog.OldestUnvendedSTS = max(0, now.Sub(*oldestUnvended))
	}
	return backlog, nil
}
