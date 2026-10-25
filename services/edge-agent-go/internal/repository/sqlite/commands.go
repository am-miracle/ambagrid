package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
)

// Every ledger transition the platform must hear about queues its report in
// the same transaction, so a crash cannot record an outcome without its report.

// recoverInterruptedCommands runs at startup, when no process can still be
// waiting on an in_flight command's reply.
func (s *Store) recoverInterruptedCommands(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE command_ledger
		SET state = 'unknown', updated_at_ms = ?,
		    last_error = COALESCE(last_error, 'agent stopped before the device outcome was recorded')
		WHERE state = 'in_flight'`, time.Now().UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("recover interrupted commands: %w", err)
	}
	return nil
}

func (s *Store) LoadCommand(ctx context.Context, commandID string) (adapter.LedgerEntry, bool, error) {
	var (
		entry                 adapter.LedgerEntry
		commandType, state    string
		acceptedMS, updatedMS int64
		requestedMS           sql.NullInt64
		completedMS           sql.NullInt64
		result                []byte
		lastError             sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT command_id, device_id, command_type, request_hash, requested_at_ms, state, attempt_count,
		       accepted_at_ms, updated_at_ms, completed_at_ms, result, last_error
		FROM command_ledger WHERE command_id = ?`, commandID).Scan(
		&entry.CommandID, &entry.DeviceID, &commandType, &entry.RequestHash, &requestedMS, &state, &entry.Attempts,
		&acceptedMS, &updatedMS, &completedMS, &result, &lastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return adapter.LedgerEntry{}, false, nil
	}
	if err != nil {
		return adapter.LedgerEntry{}, false, fmt.Errorf("load command %q: %w", commandID, err)
	}
	entry.Type = adapter.CommandType(commandType)
	entry.State = adapter.CommandState(state)
	entry.AcceptedAt = time.UnixMilli(acceptedMS).UTC()
	entry.UpdatedAt = time.UnixMilli(updatedMS).UTC()
	if requestedMS.Valid {
		entry.RequestedAt = time.UnixMilli(requestedMS.Int64).UTC()
	}
	if completedMS.Valid {
		entry.CompletedAt = time.UnixMilli(completedMS.Int64).UTC()
	}
	entry.LastError = lastError.String
	if result != nil {
		var decoded adapter.CommandResult
		if err := json.Unmarshal(result, &decoded); err != nil {
			return adapter.LedgerEntry{}, false, fmt.Errorf("decode result of command %q: %w", commandID, err)
		}
		entry.Result = &decoded
	}
	return entry, true, nil
}

func (s *Store) BeginCommandAttempt(ctx context.Context, entry adapter.LedgerEntry, at time.Time) error {
	atMS := at.UTC().UnixMilli()
	return s.commandTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO command_ledger(
				command_id, device_id, command_type, request_hash, requested_at_ms, state, accepted_at_ms, updated_at_ms
			) VALUES (?, ?, ?, ?, ?, 'in_flight', ?, ?)
			ON CONFLICT(command_id) DO UPDATE
			SET state = 'in_flight', attempt_count = attempt_count + 1, updated_at_ms = excluded.updated_at_ms
			WHERE state IN ('in_flight', 'unknown') AND request_hash = excluded.request_hash`,
			entry.CommandID, entry.DeviceID, string(entry.Type), entry.RequestHash, nullMillis(entry.RequestedAt), atMS, atMS,
		)
		if err != nil {
			return fmt.Errorf("begin command %q: %w", entry.CommandID, err)
		}
		if err := requireOneRow(result, fmt.Errorf("%w: command %q is already resolved or was recorded with different parameters", adapter.ErrInvalidCommand, entry.CommandID)); err != nil {
			return err
		}
		return queueReport(ctx, tx, entry.CommandID, domain.ReportSent, "", at)
	})
}

func (s *Store) CompleteCommand(ctx context.Context, commandID string, state adapter.CommandState, result *adapter.CommandResult, lastError string, at time.Time) error {
	report := domain.ReportAcknowledged
	switch state {
	case adapter.CommandApplied:
	case adapter.CommandRejected:
		report = domain.ReportFailed
	default:
		return fmt.Errorf("complete command %q: %q is not a terminal state", commandID, state)
	}
	var encoded []byte
	if result != nil {
		var err error
		if encoded, err = json.Marshal(result); err != nil {
			return fmt.Errorf("encode result of command %q: %w", commandID, err)
		}
	}
	atMS := at.UTC().UnixMilli()
	return s.commandTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE command_ledger
			SET state = ?, result = ?, last_error = ?, completed_at_ms = ?, updated_at_ms = ?
			WHERE command_id = ? AND state IN ('in_flight', 'unknown')`,
			string(state), encoded, nullString(lastError), atMS, atMS, commandID,
		)
		if err != nil {
			return fmt.Errorf("complete command %q: %w", commandID, err)
		}
		if err := requireOneRow(res, fmt.Errorf("complete command %q: no unresolved entry", commandID)); err != nil {
			return err
		}
		reportedAt := at
		if result != nil && !result.AcknowledgedAt.IsZero() {
			reportedAt = result.AcknowledgedAt
		}
		return queueReport(ctx, tx, commandID, report, lastError, reportedAt)
	})
}

// MarkCommandUnknown queues no report: the platform keeps the command at
// "sent", which is what its reconciliation view looks for.
func (s *Store) MarkCommandUnknown(ctx context.Context, commandID, lastError string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE command_ledger SET state = 'unknown', last_error = ?, updated_at_ms = ?
		WHERE command_id = ? AND state = 'in_flight'`,
		nullString(lastError), at.UTC().UnixMilli(), commandID,
	)
	if err != nil {
		return fmt.Errorf("mark command %q unknown: %w", commandID, err)
	}
	return requireOneRow(res, fmt.Errorf("mark command %q unknown: no in-flight entry", commandID))
}

// RefuseCommand records a command the edge will never send. It reports false
// without changes when the command already has an entry, because a command
// that may have reached the device must not be reported as never sent.
func (s *Store) RefuseCommand(ctx context.Context, entry adapter.LedgerEntry, status domain.CommandReportStatus, reason string, at time.Time) (bool, error) {
	if status != domain.ReportFailed && status != domain.ReportExpired {
		return false, fmt.Errorf("refuse command %q: %q is not a refusal", entry.CommandID, status)
	}
	atMS := at.UTC().UnixMilli()
	refused := false
	err := s.commandTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO command_ledger(
				command_id, device_id, command_type, request_hash, requested_at_ms, state,
				accepted_at_ms, updated_at_ms, completed_at_ms, last_error
			) VALUES (?, ?, ?, ?, ?, 'rejected', ?, ?, ?, ?)
			ON CONFLICT(command_id) DO NOTHING`,
			entry.CommandID, entry.DeviceID, string(entry.Type), entry.RequestHash, nullMillis(entry.RequestedAt),
			atMS, atMS, atMS, reason,
		)
		if err != nil {
			return fmt.Errorf("refuse command %q: %w", entry.CommandID, err)
		}
		if affected, err := res.RowsAffected(); err != nil || affected == 0 {
			return err
		}
		refused = true
		reportReason := ""
		if status == domain.ReportFailed {
			reportReason = reason
		}
		return queueReport(ctx, tx, entry.CommandID, status, reportReason, at)
	})
	return refused, err
}

// LastRelayCommand returns the newest relay command for deviceID that was or
// may have been sent. Rejected commands are excluded because they never acted.
// Pruning can drop old entries; command expiry keeps far older commands from
// arriving, so the newest surviving entry still decides.
func (s *Store) LastRelayCommand(ctx context.Context, deviceID string) (requestedAt time.Time, commandID string, found bool, err error) {
	var requestedMS int64
	err = s.db.QueryRowContext(ctx, `
		SELECT requested_at_ms, command_id FROM command_ledger
		WHERE device_id = ? AND command_type IN ('disconnect_meter', 'reconnect_meter')
		  AND state <> 'rejected' AND requested_at_ms IS NOT NULL
		ORDER BY requested_at_ms DESC, command_id DESC LIMIT 1`, deviceID).Scan(&requestedMS, &commandID)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", false, nil
	}
	if err != nil {
		return time.Time{}, "", false, fmt.Errorf("find last relay command for %q: %w", deviceID, err)
	}
	return time.UnixMilli(requestedMS).UTC(), commandID, true, nil
}

// DueCommandReports holds back a report while an earlier one for the same
// command is still waiting, so the platform hears "sent" before "acknowledged".
func (s *Store) DueCommandReports(ctx context.Context, now time.Time, limit int) ([]domain.CommandReport, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.command_id, r.status, r.occurred_at_ms, r.failure_reason, r.attempt_count
		FROM command_reports r
		WHERE r.abandoned_at_ms IS NULL AND r.next_attempt_at_ms <= ?
		  AND NOT EXISTS (
		      SELECT 1 FROM command_reports earlier
		      WHERE earlier.command_id = r.command_id AND earlier.id < r.id AND earlier.abandoned_at_ms IS NULL
		  )
		ORDER BY r.id LIMIT ?`, now.UTC().UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due command reports: %w", err)
	}
	defer rows.Close()
	var reports []domain.CommandReport
	for rows.Next() {
		var (
			report     domain.CommandReport
			status     string
			occurredMS int64
			reason     sql.NullString
		)
		if err := rows.Scan(&report.ID, &report.CommandID, &status, &occurredMS, &reason, &report.Attempts); err != nil {
			return nil, fmt.Errorf("scan command report: %w", err)
		}
		report.Status = domain.CommandReportStatus(status)
		report.OccurredAt = time.UnixMilli(occurredMS).UTC()
		report.FailureReason = reason.String
		reports = append(reports, report)
	}
	return reports, rows.Err()
}

func (s *Store) DeliveredCommandReport(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM command_reports WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete delivered command report %d: %w", id, err)
	}
	return nil
}

func (s *Store) DeferCommandReport(ctx context.Context, id int64, next time.Time, lastError string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE command_reports SET attempt_count = attempt_count + 1, next_attempt_at_ms = ?, last_error = ?
		WHERE id = ?`, next.UTC().UnixMilli(), nullString(lastError), id)
	if err != nil {
		return fmt.Errorf("defer command report %d: %w", id, err)
	}
	return nil
}

// AbandonCommandReport keeps a report the platform refused for good, so the
// evidence survives for reconciliation instead of being retried forever.
func (s *Store) AbandonCommandReport(ctx context.Context, id int64, at time.Time, lastError string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE command_reports SET attempt_count = attempt_count + 1, abandoned_at_ms = ?, last_error = ?
		WHERE id = ?`, at.UTC().UnixMilli(), nullString(lastError), id)
	if err != nil {
		return fmt.Errorf("abandon command report %d: %w", id, err)
	}
	return nil
}

// PruneCommands deletes at most limit resolved commands completed before
// cutoff, and abandoned reports older than cutoff. Unresolved commands are
// kept until someone reconciles them.
func (s *Store) PruneCommands(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	cutoffMS := cutoff.UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM command_ledger WHERE command_id IN (
			SELECT command_id FROM command_ledger
			WHERE state IN ('applied', 'rejected') AND completed_at_ms < ?
			ORDER BY completed_at_ms LIMIT ?
		)`, cutoffMS, limit)
	if err != nil {
		return 0, fmt.Errorf("prune resolved commands: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read pruned command count: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM command_reports WHERE id IN (
			SELECT id FROM command_reports WHERE abandoned_at_ms < ? ORDER BY id LIMIT ?
		)`, cutoffMS, limit); err != nil {
		return deleted, fmt.Errorf("prune abandoned command reports: %w", err)
	}
	return deleted, nil
}

func (s *Store) CommandLedgerStats(ctx context.Context, now time.Time) (domain.CommandLedgerStats, error) {
	var (
		stats    domain.CommandLedgerStats
		oldestMS sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM command_ledger WHERE state IN ('in_flight', 'unknown')),
			(SELECT MIN(accepted_at_ms) FROM command_ledger WHERE state IN ('in_flight', 'unknown')),
			(SELECT COUNT(*) FROM command_reports WHERE abandoned_at_ms IS NULL),
			(SELECT COUNT(*) FROM command_reports WHERE abandoned_at_ms IS NOT NULL)`).Scan(
		&stats.Unresolved, &oldestMS, &stats.PendingReports, &stats.AbandonedReports)
	if err != nil {
		return domain.CommandLedgerStats{}, fmt.Errorf("read command ledger stats: %w", err)
	}
	if oldestMS.Valid {
		stats.OldestUnresolvedAge = max(0, now.Sub(time.UnixMilli(oldestMS.Int64)))
	}
	return stats, nil
}

func (s *Store) commandTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin command ledger transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit command ledger transaction: %w", err)
	}
	return nil
}

func queueReport(ctx context.Context, tx *sql.Tx, commandID string, status domain.CommandReportStatus, reason string, at time.Time) error {
	if status == domain.ReportFailed && reason == "" {
		reason = "rejected without a reason"
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO command_reports(command_id, status, occurred_at_ms, failure_reason, next_attempt_at_ms)
		VALUES (?, ?, ?, ?, ?)`,
		commandID, string(status), at.UTC().UnixMilli(), nullString(reason), at.UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("queue %s report for command %q: %w", status, commandID, err)
	}
	return nil
}

func requireOneRow(result sql.Result, unchanged error) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if affected != 1 {
		return unchanged
	}
	return nil
}

func nullMillis(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().UnixMilli()
}
