package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"edge-agent-go/internal/fallback"
)

const heartbeatAttemptRetention = 7 * 24 * time.Hour

func (s *Store) HeartbeatState(ctx context.Context, now time.Time) (fallback.HeartbeatState, error) {
	var lastSuccess, lastAttempt, lastSucceeded sql.NullInt64
	var state fallback.HeartbeatState
	err := s.db.QueryRowContext(ctx, `
		SELECT s.last_upload_success_at_ms,
		       latest.attempted_at_ms, latest.succeeded,
		       (SELECT COUNT(*) FROM heartbeat_attempts WHERE attempted_at_ms >= ?)
		FROM edge_sync_state s
		LEFT JOIN (
		    SELECT attempted_at_ms, succeeded FROM heartbeat_attempts ORDER BY sequence DESC LIMIT 1
		) latest ON true
		WHERE s.singleton = 1`, now.Add(-24*time.Hour).UnixMilli()).
		Scan(&lastSuccess, &lastAttempt, &lastSucceeded, &state.AttemptsLastDay)
	if err != nil {
		return fallback.HeartbeatState{}, fmt.Errorf("read heartbeat state: %w", err)
	}
	if lastSuccess.Valid {
		value := time.UnixMilli(lastSuccess.Int64).UTC()
		state.LastUploadSuccess = &value
	}
	if lastAttempt.Valid {
		value := time.UnixMilli(lastAttempt.Int64).UTC()
		state.LastAttemptAt = &value
	}
	state.LastAttemptFailed = lastSucceeded.Valid && lastSucceeded.Int64 == 0
	return state, nil
}

// ReserveHeartbeat persists the attempt before sending so a crash mid-send
// still consumes the sequence and counts against the daily budget.
func (s *Store) ReserveHeartbeat(ctx context.Context, attemptedAt time.Time) (uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin reserve heartbeat: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM heartbeat_attempts WHERE attempted_at_ms < ?`,
		attemptedAt.Add(-heartbeatAttemptRetention).UnixMilli()); err != nil {
		return 0, fmt.Errorf("prune heartbeat attempts: %w", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO heartbeat_attempts(attempted_at_ms) VALUES (?)`, attemptedAt.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("reserve heartbeat: %w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read heartbeat sequence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit reserve heartbeat: %w", err)
	}
	return uint64(sequence), nil
}

func (s *Store) RecordHeartbeat(ctx context.Context, sequence uint64, failureReason string) error {
	var reason any
	if failureReason != "" {
		reason = failureReason
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE heartbeat_attempts SET succeeded = ?, failure_reason = ? WHERE sequence = ?`,
		failureReason == "", reason, sequence)
	if err != nil {
		return fmt.Errorf("record heartbeat: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return fmt.Errorf("read heartbeat record result: %w", err)
		}
		return fmt.Errorf("heartbeat reservation %d not found", sequence)
	}
	return nil
}
