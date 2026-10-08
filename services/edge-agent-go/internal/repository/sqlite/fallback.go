package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"edge-agent-go/internal/fallback"
)

func (s *Store) recoverFallbackIncidents(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE queue_events AS q SET critical_code = NULL, critical_value = NULL
		WHERE q.critical_code IS NOT NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM fallback_incidents own
		      WHERE own.event_key = 'edge:' || q.site_id || ':' || q.sequence
		  )
		  AND EXISTS (
		      SELECT 1 FROM fallback_incidents i
		      WHERE i.code = q.critical_code
		        AND i.asset_id = CASE WHEN q.critical_code = 'SITE_OUTAGE' THEN 'site' ELSE q.device_id END
		        AND i.event_key <> 'edge:' || q.site_id || ':' || q.sequence
		        AND i.sequence < q.sequence
		        AND (i.resolved_at_ms IS NULL OR i.resolved_at_ms >= q.event_at_ms)
		  )`)
	if err != nil {
		return fmt.Errorf("suppress recovered fallback duplicates: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO fallback_incidents(
			event_key, sequence, site_id, asset_id, code, temperature_c, event_at_ms, opened_at_ms
		)
		SELECT 'edge:' || site_id || ':' || sequence, sequence, site_id,
		       CASE WHEN critical_code = 'SITE_OUTAGE' THEN 'site' ELSE device_id END,
		       critical_code, critical_value, event_at_ms, persisted_at_ms
		FROM queue_events
		WHERE critical_code IS NOT NULL AND status IN ('persisted', 'pending_upload', 'uploaded')`)
	if err != nil {
		return fmt.Errorf("recover fallback incidents: %w", err)
	}
	return nil
}

func (s *Store) OpenFallbackIncident(ctx context.Context, event fallback.Event, openedAt time.Time) error {
	message, err := fallback.Format(event)
	if err != nil {
		return err
	}
	_ = message
	_, err = s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO fallback_incidents(
			event_key, sequence, site_id, asset_id, code, temperature_c, event_at_ms, opened_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventKey(), event.Sequence, event.SiteID, event.AssetID, string(event.Code), event.TemperatureC,
		event.OccurredAt.UnixMilli(), openedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("open fallback incident: %w", err)
	}
	return nil
}

func (s *Store) ResolveFallbackIncident(ctx context.Context, assetID string, code fallback.Code, resolvedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE fallback_incidents SET resolved_at_ms = ?
		WHERE asset_id = ? AND code = ? AND resolved_at_ms IS NULL`, resolvedAt.UnixMilli(), assetID, string(code))
	if err != nil {
		return fmt.Errorf("resolve fallback incident: %w", err)
	}
	return nil
}

func (s *Store) RecordFallbackUploadFailure(ctx context.Context, sequence uint64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE fallback_incidents SET upload_failures = upload_failures + 1
		WHERE sequence = ? AND resolved_at_ms IS NULL AND normal_delivered_at_ms IS NULL`, sequence)
	if err != nil {
		return fmt.Errorf("record fallback upload failure: %w", err)
	}
	return nil
}

func (s *Store) RecordFallbackUploadSuccess(ctx context.Context, sequence uint64, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fallback upload success: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE fallback_incidents SET normal_delivered_at_ms = ? WHERE sequence = ?`, at.UnixMilli(), sequence); err != nil {
		return fmt.Errorf("mark fallback normally delivered: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE edge_sync_state SET last_upload_success_at_ms = ? WHERE singleton = 1`, at.UnixMilli()); err != nil {
		return fmt.Errorf("record upload success: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fallback upload success: %w", err)
	}
	return nil
}

func (s *Store) RecordUploadContactSuccess(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE edge_sync_state SET last_upload_success_at_ms = ? WHERE singleton = 1`, at.UnixMilli())
	if err != nil {
		return fmt.Errorf("record upload contact success: %w", err)
	}
	return nil
}

func (s *Store) FallbackCandidates(ctx context.Context, now time.Time) ([]fallback.Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.site_id, i.asset_id, i.sequence, i.code, i.temperature_c, i.event_at_ms,
		       i.opened_at_ms, i.upload_failures,
		       (SELECT COUNT(*) FROM fallback_attempts own_attempt WHERE own_attempt.event_key = i.event_key),
		       i.next_sms_attempt_at_ms,
		       s.last_upload_success_at_ms,
		       (SELECT COUNT(*) FROM fallback_attempts a WHERE a.attempted_at_ms >= ?),
		       (SELECT COUNT(*) FROM fallback_attempts a WHERE a.attempted_at_ms >= ?)
		FROM fallback_incidents i CROSS JOIN edge_sync_state s
		WHERE i.resolved_at_ms IS NULL AND i.normal_delivered_at_ms IS NULL AND i.sms_sent_at_ms IS NULL
		ORDER BY i.opened_at_ms, i.sequence`, now.Add(-time.Hour).UnixMilli(), now.Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("list fallback candidates: %w", err)
	}
	defer rows.Close()
	var candidates []fallback.Candidate
	for rows.Next() {
		var candidate fallback.Candidate
		var code string
		var temperature sql.NullFloat64
		var eventAt, openedAt int64
		var nextAttempt, lastSuccess sql.NullInt64
		if err := rows.Scan(&candidate.SiteID, &candidate.AssetID, &candidate.Sequence, &code, &temperature,
			&eventAt, &openedAt, &candidate.UploadFailures, &candidate.Attempts, &nextAttempt, &lastSuccess,
			&candidate.AttemptsLastHour, &candidate.AttemptsLastDay); err != nil {
			return nil, fmt.Errorf("scan fallback candidate: %w", err)
		}
		candidate.Code = fallback.Code(code)
		candidate.OccurredAt = time.UnixMilli(eventAt).UTC()
		candidate.OpenedAt = time.UnixMilli(openedAt).UTC()
		if temperature.Valid {
			value := temperature.Float64
			candidate.TemperatureC = &value
		}
		if nextAttempt.Valid {
			value := time.UnixMilli(nextAttempt.Int64).UTC()
			candidate.NextAttemptAt = &value
		}
		if lastSuccess.Valid {
			value := time.UnixMilli(lastSuccess.Int64).UTC()
			candidate.LastUploadSuccess = &value
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read fallback candidates: %w", err)
	}
	return candidates, nil
}

func (s *Store) RecordFallbackAttempt(ctx context.Context, result fallback.AttemptResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fallback attempt: %w", err)
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, `
		UPDATE fallback_attempts SET succeeded = ?, failure_reason = ?
		WHERE event_key = ? AND attempted_at_ms = ?`, result.Succeeded, result.FailureReason, result.EventKey, result.AttemptedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("finish fallback attempt: %w", err)
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return fmt.Errorf("read fallback attempt result: %w", err)
		}
		return fmt.Errorf("fallback attempt reservation not found for %s", result.EventKey)
	}
	var nextAttempt any
	if result.NextAttemptAt != nil {
		nextAttempt = result.NextAttemptAt.UnixMilli()
	}
	var sentAt any
	if result.Succeeded {
		sentAt = result.AttemptedAt.UnixMilli()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE fallback_incidents SET sms_attempt_count = sms_attempt_count + 1,
			next_sms_attempt_at_ms = ?, sms_sent_at_ms = COALESCE(sms_sent_at_ms, ?), last_error = ?
		WHERE event_key = ?`, nextAttempt, sentAt, result.FailureReason, result.EventKey); err != nil {
		return fmt.Errorf("update fallback attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fallback attempt: %w", err)
	}
	return nil
}

func (s *Store) ReserveFallbackAttempt(ctx context.Context, eventKey string, attemptedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO fallback_attempts(event_key, attempted_at_ms) VALUES (?, ?)`, eventKey, attemptedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("reserve fallback attempt: %w", err)
	}
	return nil
}
