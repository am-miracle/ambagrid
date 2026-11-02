package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"api-go/internal/domain"
	"api-go/internal/repository"
)

const meterCommandColumns = `mc.command_id::text, mc.meter_id, mc.command_type, mc.status,
    mc.requested_by, mc.reason, mc.requested_at, mc.expires_at, mc.sent_at,
    mc.acknowledged_at, mc.failure_reason, mc.credit_kwh::float8, mc.credit_id::text`

// Only a requested command has certainly never reached the edge. A queued one
// may have been sent, so the platform expiring it could contradict the device.
const expireOverdueCommandsSQL = `
UPDATE meter_commands mc SET status = 'expired'
FROM smart_meters sm
WHERE sm.meter_id = mc.meter_id
  AND sm.site_id = $1
  AND mc.status = 'requested'
  AND mc.expires_at <= $2
RETURNING ` + meterCommandColumns

// Unsent commands go first so unresolved sent ones cannot consume the batch.
// Overdue queued commands are redelivered so the edge settles them.
const listPendingEdgeCommandsSQL = `
SELECT mc.command_id::text, mc.meter_id, sm.asset_id, mc.command_type,
    mc.status, mc.requested_at, mc.expires_at, mc.credit_kwh::float8
FROM meter_commands mc
JOIN smart_meters sm ON sm.meter_id = mc.meter_id
WHERE sm.site_id = $1
  AND mc.status IN ('requested', 'queued', 'sent')
  AND (mc.status = 'queued' OR mc.expires_at > $2)
  -- Never dispatch an unsent replacement after any attempt gains positive or
  -- uncertain delivery evidence. Already queued commands must still settle at the edge.
  AND NOT (mc.status='requested' AND mc.replaces_command_id IS NOT NULL AND (
    EXISTS(SELECT 1 FROM meter_commands applied WHERE applied.credit_id=mc.credit_id AND applied.status='acknowledged')
    OR EXISTS(SELECT 1 FROM meter_commands prior JOIN audit_events a ON a.subject_id=prior.command_id::text
      WHERE prior.credit_id=mc.credit_id AND ` + uncertainDeliveryAuditPredicate + `)
    OR EXISTS(SELECT 1 FROM meter_commands prior JOIN command_reconciliation_cases c USING(command_id)
      WHERE prior.credit_id=mc.credit_id AND c.disposition='delivered')
  ))
ORDER BY (mc.status = 'sent'), mc.requested_at, mc.command_id
LIMIT $3
FOR UPDATE OF mc`

const markCommandsQueuedSQL = `
UPDATE meter_commands SET status = 'queued'
WHERE command_id = ANY($1::uuid[]) AND status = 'requested'`

const lockSiteCommandSQL = `
SELECT ` + meterCommandColumns + `
FROM meter_commands mc
JOIN smart_meters sm ON sm.meter_id = mc.meter_id
WHERE mc.command_id = $1::uuid AND sm.site_id = $2
FOR UPDATE OF mc`

const saveCommandStatusSQL = `
UPDATE meter_commands
SET status = $2, sent_at = $3, acknowledged_at = $4, failure_reason = $5
WHERE command_id = $1::uuid`

type EdgeCommandStore struct {
	pool         databasePool
	queryTimeout time.Duration
}

func NewEdgeCommandStore(pool databasePool, queryTimeout time.Duration) *EdgeCommandStore {
	return &EdgeCommandStore{pool: pool, queryTimeout: queryTimeout}
}

func (s *EdgeCommandStore) RunEdgeCommandTx(ctx context.Context, fn func(repository.EdgeCommandTx) error) error {
	if s.queryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.queryTimeout)
		defer cancel()
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin edge command transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(edgeCommandTx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit edge command transaction: %w", err)
	}
	return nil
}

type edgeCommandTx struct {
	tx pgx.Tx
}

func (t edgeCommandTx) ExpireOverdueCommands(ctx context.Context, siteID string, now time.Time) ([]domain.MeterCommand, error) {
	if err := lockSiteCommandDelivery(ctx, t.tx, siteID); err != nil {
		return nil, err
	}
	rows, err := t.tx.Query(ctx, expireOverdueCommandsSQL, siteID, now)
	if err != nil {
		return nil, fmt.Errorf("expire overdue meter commands: %w", err)
	}
	defer rows.Close()
	var expired []domain.MeterCommand
	for rows.Next() {
		cmd, err := scanMeterCommand(rows)
		if err != nil {
			return nil, err
		}
		expired = append(expired, cmd)
	}
	return expired, rows.Err()
}

func (t edgeCommandTx) ListPendingEdgeCommands(ctx context.Context, siteID string, now time.Time, limit int) ([]domain.EdgeCommand, error) {
	if err := lockSiteCommandDelivery(ctx, t.tx, siteID); err != nil {
		return nil, err
	}
	rows, err := t.tx.Query(ctx, listPendingEdgeCommandsSQL, siteID, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending edge commands: %w", err)
	}
	defer rows.Close()
	var commands []domain.EdgeCommand
	for rows.Next() {
		var cmd domain.EdgeCommand
		if err := rows.Scan(&cmd.CommandID, &cmd.MeterID, &cmd.DeviceID, &cmd.CommandType, &cmd.Status, &cmd.RequestedAt, &cmd.ExpiresAt, &cmd.CreditKWh); err != nil {
			return nil, fmt.Errorf("scan pending edge command: %w", err)
		}
		commands = append(commands, cmd)
	}
	return commands, rows.Err()
}

func (t edgeCommandTx) MarkCommandsQueued(ctx context.Context, commandIDs []string) error {
	if len(commandIDs) == 0 {
		return nil
	}
	if _, err := t.tx.Exec(ctx, markCommandsQueuedSQL, commandIDs); err != nil {
		return fmt.Errorf("mark meter commands queued: %w", err)
	}
	return nil
}

func (t edgeCommandTx) LockSiteCommand(ctx context.Context, siteID, commandID string) (domain.MeterCommand, error) {
	if err := lockSiteCommandDelivery(ctx, t.tx, siteID); err != nil {
		return domain.MeterCommand{}, err
	}
	cmd, err := scanMeterCommand(t.tx.QueryRow(ctx, lockSiteCommandSQL, commandID, siteID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MeterCommand{}, fmt.Errorf("%w: meter command %s at site %s", domain.ErrNotFound, commandID, siteID)
	}
	if err != nil {
		return cmd, fmt.Errorf("lock meter command %s: %w", commandID, err)
	}
	if cmd.Status == domain.CommandStatusAcknowledged {
		cmd.Observation, cmd.ObservedAt, _, err = readCommandObservation(ctx, t.tx, commandID)
		if err != nil {
			return cmd, err
		}
	}
	return cmd, nil
}

func (t edgeCommandTx) SaveCommandStatus(ctx context.Context, cmd domain.MeterCommand) error {
	if cmd.Observation != nil {
		o := cmd.Observation
		if _, err := t.tx.Exec(ctx, `INSERT INTO meter_command_observations(command_id,observed_at,relay_closed,balance_kwh,tamper_detected,firmware_version,fault_code)
   VALUES($1::uuid,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,'')) ON CONFLICT(command_id) DO NOTHING`, cmd.CommandID, cmd.ObservedAt, o.RelayClosed, o.BalanceKWh, o.TamperDetected, o.FirmwareVersion, o.FaultCode); err != nil {
			return fmt.Errorf("save command observation: %w", err)
		}
	}

	if _, err := t.tx.Exec(ctx, saveCommandStatusSQL, cmd.CommandID, cmd.Status, cmd.SentAt, cmd.AcknowledgedAt, cmd.FailureReason); err != nil {
		return fmt.Errorf("save meter command %s status: %w", cmd.CommandID, err)
	}
	return nil
}

func (t edgeCommandTx) PublishCommandOutcome(ctx context.Context, topic, eventType string, outcome domain.CommandOutcome) error {
	cmd := outcome.Command
	payload, err := meterCommandOutcomePayload(outcome)
	if err != nil {
		return err
	}
	if _, err := t.tx.Exec(ctx, insertCommandEventSQL, topic, eventType, "meter_command", cmd.CommandID, string(payload)); err != nil {
		return fmt.Errorf("insert %s event for command %s: %w", eventType, cmd.CommandID, err)
	}
	return nil
}

// meterCommandOutcomePayload matches proto/fixtures/meter_command_*_outbox_payload.json.
func meterCommandOutcomePayload(outcome domain.CommandOutcome) ([]byte, error) {
	cmd := outcome.Command
	payload, err := json.Marshal(map[string]any{
		"command_id":     cmd.CommandID,
		"meter_id":       cmd.MeterID,
		"site_id":        outcome.SiteID,
		"command_type":   cmd.CommandType,
		"status":         cmd.Status,
		"reported_by":    outcome.ReportedBy,
		"failure_reason": cmd.FailureReason,
		"occurred_at":    outcome.OccurredAt.UTC().Format(time.RFC3339),
		"credit_kwh":     cmd.CreditKWh,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal meter command outcome: %w", err)
	}
	return payload, nil
}

func (t edgeCommandTx) InsertAuditEvent(ctx context.Context, siteID, actorID, action, subjectType, subjectID string, metadata map[string]any) error {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	if _, err := t.tx.Exec(ctx, insertAuditEventSQL, siteID, actorID, action, subjectType, subjectID, string(metadataJSON)); err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

func scanMeterCommand(row pgx.Row) (domain.MeterCommand, error) {
	var cmd domain.MeterCommand
	err := row.Scan(
		&cmd.CommandID, &cmd.MeterID, &cmd.CommandType, &cmd.Status,
		&cmd.RequestedBy, &cmd.Reason, &cmd.RequestedAt, &cmd.ExpiresAt,
		&cmd.SentAt, &cmd.AcknowledgedAt, &cmd.FailureReason, &cmd.CreditKWh, &cmd.CreditID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.MeterCommand{}, err
		}
		return domain.MeterCommand{}, fmt.Errorf("scan meter command: %w", err)
	}
	return cmd, nil
}
