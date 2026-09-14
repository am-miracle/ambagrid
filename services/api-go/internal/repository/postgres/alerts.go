// reads alerts and their resolution history from Postgres.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"api-go/internal/domain"
	"api-go/internal/page"
)

const alertSelect = `
SELECT alert_id::text,
       asset_id,
       site_id,
       kind,
       severity,
       status,
       reason,
       opened_at,
       source_event_id,
       resolved_at,
       resolution_note,
       resolved_by
FROM alerts`

const getAlertSQL = alertSelect + `
WHERE alert_id = $1::uuid`

const listAlertResolutionsSQL = `
SELECT resolution_id::text,
       resolved_at,
       resolution_note,
       resolved_by,
       recorded_at
FROM alert_resolution_history
WHERE alert_id = $1::uuid
ORDER BY recorded_at DESC`

const resolveAlertSQL = `
UPDATE alerts
SET status = 'resolved',
    resolved_at = $2,
    resolution_note = $3,
    resolved_by = $4,
    updated_at = $2
WHERE alert_id = $1::uuid
  AND status = 'open'
RETURNING alert_id::text,
       asset_id,
       site_id,
       kind,
       severity,
       status,
       reason,
       opened_at,
       source_event_id,
       resolved_at,
       resolution_note,
       resolved_by`

const insertAlertResolutionSQL = `
INSERT INTO alert_resolution_history (
    alert_id, resolved_at, resolution_note, resolved_by
)
VALUES ($1::uuid, $2, $3, $4)`

const insertCommandEventSQL = `
INSERT INTO command_event_outbox (
    topic, event_type, aggregate_type, aggregate_id, payload
)
VALUES ($1, $2, $3, $4, $5::jsonb)`

const alertExistsSQL = `
SELECT EXISTS (
    SELECT 1 FROM alerts WHERE alert_id = $1::uuid
)`

const alertResolvedEventType = "alert.resolved"

func (s *Store) ListAlerts(ctx context.Context, query domain.AlertQuery) (page.Page[domain.Alert], error) {
	var (
		afterOpenedAt *time.Time
		afterAlertID  *string
	)
	cursor, err := page.DecodeTimeIDCursor(query.Cursor, domain.ValidateAlertID)
	if err != nil {
		return page.Page[domain.Alert]{}, err
	}
	if cursor != nil {
		afterOpenedAt = &cursor.Time
		afterAlertID = &cursor.ID
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	// +1 request extra row to know if more pages exist without COUNT(*)
	sql, args := buildListAlertsQuery(query.Filter, afterOpenedAt, afterAlertID, query.Limit+1)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return page.Page[domain.Alert]{}, fmt.Errorf("query alerts: %w", err)
	}
	defer rows.Close()

	alerts := make([]domain.Alert, 0, query.Limit+1)
	for rows.Next() {
		alert, err := scanAlert(rows)
		if err != nil {
			return page.Page[domain.Alert]{}, err
		}
		alerts = append(alerts, alert)
	}
	if err := rows.Err(); err != nil {
		return page.Page[domain.Alert]{}, fmt.Errorf("read alerts: %w", err)
	}

	return page.Build(alerts, query.Limit, func(a domain.Alert) string {
		return page.TimeIDCursor{Time: a.OpenedAt, ID: a.AlertID}.Encode()
	}), nil
}

// Alert ID breaks opened_at ties for stable keyset pagination.
func buildListAlertsQuery(filter domain.AlertFilter, afterOpenedAt *time.Time, afterAlertID *string, limit int) (string, []any) {
	b := newPredicates()

	switch {
	// A literal lets Postgres use the partial index for open alerts.
	case filter.Status != nil && *filter.Status == domain.AlertStatusOpen:
		b.addLiteral("status = 'open'")
	case filter.Status != nil:
		b.add("status = ", string(*filter.Status))
	}
	if filter.SiteID != nil {
		b.add("site_id = ", *filter.SiteID)
	}
	if filter.AssetID != nil {
		b.add("asset_id = ", *filter.AssetID)
	}
	if filter.Severity != nil {
		b.add("severity = ", string(*filter.Severity))
	}
	if filter.Kind != nil {
		b.add("kind = ", *filter.Kind)
	}
	if afterOpenedAt != nil && afterAlertID != nil {
		openedAt := b.bind(*afterOpenedAt)
		alertID := b.bind(*afterAlertID)
		b.addLiteral("(opened_at, alert_id) < (" + openedAt + "::timestamptz, " + alertID + "::uuid)")
	}

	return b.finish(alertSelect, "ORDER BY opened_at DESC, alert_id DESC", limit)
}

func (s *Store) GetAlertWithResolutions(ctx context.Context, alertID string) (domain.Alert, []domain.AlertResolution, error) {
	if err := domain.ValidateAlertID(alertID); err != nil {
		return domain.Alert{}, nil, err
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.Alert{}, nil, fmt.Errorf("begin alert detail transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	alert, err := scanAlert(tx.QueryRow(ctx, getAlertSQL, alertID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Alert{}, nil, fmt.Errorf("%w: alert %q", domain.ErrNotFound, alertID)
	}
	if err != nil {
		return domain.Alert{}, nil, err
	}

	// Use one snapshot and close the result set before committing.
	resolutions, err := queryAlertResolutions(ctx, tx, alertID)
	if err != nil {
		return domain.Alert{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Alert{}, nil, fmt.Errorf("commit alert detail transaction: %w", err)
	}
	return alert, resolutions, nil
}

func (s *Store) ResolveAlert(ctx context.Context, command domain.ResolveAlertCommand) (domain.Alert, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return domain.Alert{}, fmt.Errorf("begin alert resolution transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resolvedAt := time.Now().UTC()
	alert, err := scanAlert(tx.QueryRow(ctx, resolveAlertSQL, command.AlertID, resolvedAt, command.ResolutionNote, command.ResolvedBy))
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if existsErr := tx.QueryRow(ctx, alertExistsSQL, command.AlertID).Scan(&exists); existsErr != nil {
			return domain.Alert{}, fmt.Errorf("check alert existence: %w", existsErr)
		}
		if !exists {
			return domain.Alert{}, fmt.Errorf("%w: alert %q", domain.ErrNotFound, command.AlertID)
		}
		return domain.Alert{}, fmt.Errorf("%w: alert %q is not open", domain.ErrConflict, command.AlertID)
	}
	if err != nil {
		return domain.Alert{}, err
	}

	if _, err := tx.Exec(ctx, insertAlertResolutionSQL, command.AlertID, resolvedAt, command.ResolutionNote, command.ResolvedBy); err != nil {
		return domain.Alert{}, fmt.Errorf("insert alert resolution history: %w", err)
	}
	if err := insertAlertResolvedEvent(ctx, tx, alert); err != nil {
		return domain.Alert{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Alert{}, fmt.Errorf("commit alert resolution transaction: %w", err)
	}
	return alert, nil
}

func insertAlertResolvedEvent(ctx context.Context, tx pgx.Tx, alert domain.Alert) error {
	payload, err := alertResolvedPayload(alert)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, insertCommandEventSQL, alertResolvedEventType, alertResolvedEventType, "alert", alert.AlertID, payload); err != nil {
		return fmt.Errorf("insert alert resolved event: %w", err)
	}
	return nil
}

func alertResolvedPayload(alert domain.Alert) ([]byte, error) {
	if alert.ResolvedAt == nil {
		return nil, fmt.Errorf("%w: resolved alert %q is missing resolved_at", domain.ErrInvalidData, alert.AlertID)
	}
	if alert.ResolutionNote == nil {
		return nil, fmt.Errorf("%w: resolved alert %q is missing resolution_note", domain.ErrInvalidData, alert.AlertID)
	}
	if alert.ResolvedBy == nil {
		return nil, fmt.Errorf("%w: resolved alert %q is missing resolved_by", domain.ErrInvalidData, alert.AlertID)
	}

	payload := map[string]any{
		"alert_id":        alert.AlertID,
		"asset_id":        alert.AssetID,
		"site_id":         alert.SiteID,
		"severity":        protoSeverity(alert.Severity),
		"reason":          alert.Reason,
		"opened_at_utc":   alert.OpenedAt.Unix(),
		"resolved_at_utc": alert.ResolvedAt.Unix(),
		"resolution_note": *alert.ResolutionNote,
		"resolved_by":     *alert.ResolvedBy,
	}
	return json.Marshal(payload)
}

func protoSeverity(severity domain.Severity) string {
	switch severity {
	case domain.SeverityInfo:
		return "SEVERITY_INFO"
	case domain.SeverityWarning:
		return "SEVERITY_WARNING"
	case domain.SeverityCritical:
		return "SEVERITY_CRITICAL"
	default:
		return "SEVERITY_UNSPECIFIED"
	}
}

func queryAlertResolutions(ctx context.Context, tx pgx.Tx, alertID string) ([]domain.AlertResolution, error) {
	rows, err := tx.Query(ctx, listAlertResolutionsSQL, alertID)
	if err != nil {
		return nil, fmt.Errorf("query alert resolutions: %w", err)
	}
	defer rows.Close()

	resolutions := make([]domain.AlertResolution, 0)
	for rows.Next() {
		var resolution domain.AlertResolution
		if err := rows.Scan(
			&resolution.ResolutionID,
			&resolution.ResolvedAt,
			&resolution.ResolutionNote,
			&resolution.ResolvedBy,
			&resolution.RecordedAt,
		); err != nil {
			return nil, fmt.Errorf("read alert resolutions: %w", err)
		}
		resolutions = append(resolutions, resolution)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read alert resolutions: %w", err)
	}

	return resolutions, nil
}

func scanAlert(row scanner) (domain.Alert, error) {
	var (
		alert    domain.Alert
		severity string
		status   string
	)

	err := row.Scan(
		&alert.AlertID,
		&alert.AssetID,
		&alert.SiteID,
		&alert.Kind,
		&severity,
		&status,
		&alert.Reason,
		&alert.OpenedAt,
		&alert.SourceEventID,
		&alert.ResolvedAt,
		&alert.ResolutionNote,
		&alert.ResolvedBy,
	)
	if err != nil {
		return domain.Alert{}, err
	}

	alert.Severity, err = parseEnum(alert.AlertID, domain.ParseSeverity, severity)
	if err != nil {
		return domain.Alert{}, err
	}
	alert.Status, err = parseEnum(alert.AlertID, domain.ParseAlertStatus, status)
	if err != nil {
		return domain.Alert{}, err
	}
	return alert, nil
}
