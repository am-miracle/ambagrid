// reads alerts and their resolution history from Postgres.
package postgres

import (
	"context"
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

func (s *Store) ListAlerts(ctx context.Context, query domain.AlertQuery) (page.Page[domain.Alert], error) {
	var (
		afterOpenedAt *time.Time
		afterAlertID  *string
	)
	if query.Cursor != "" {
		fields, err := page.Decode(query.Cursor, 2)
		if err != nil {
			return page.Page[domain.Alert]{}, err
		}
		openedAt, err := time.Parse(time.RFC3339Nano, fields[0])
		if err != nil {
			return page.Page[domain.Alert]{}, fmt.Errorf("%w: opened_at", page.ErrInvalidCursor)
		}
		if err := domain.ValidateAlertID(fields[1]); err != nil {
			return page.Page[domain.Alert]{}, fmt.Errorf("%w: alert_id", page.ErrInvalidCursor)
		}
		afterOpenedAt = &openedAt
		afterAlertID = &fields[1]
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
		return page.Encode(a.OpenedAt.UTC().Format(time.RFC3339Nano), a.AlertID)
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
