// Builds site rollups from assets and open alerts.
package postgres

import (
	"context"
	"fmt"

	"api-go/internal/domain"
	"api-go/internal/page"
)

const listSitesSQL = `
WITH asset_rollup AS (
    SELECT site_id,
           count(*) AS asset_count,
           max(last_seen_at) AS last_seen_at
    FROM assets
    GROUP BY site_id
),
open_alert_rollup AS (
    SELECT site_id,
           count(*) AS total,
           count(*) FILTER (WHERE severity = 'critical') AS critical,
           count(*) FILTER (WHERE severity = 'warning') AS warning,
           count(*) FILTER (WHERE severity = 'info') AS info
    FROM alerts
    WHERE status = 'open'
    GROUP BY site_id
)
SELECT a.site_id,
       a.asset_count,
       a.last_seen_at,
       coalesce(o.total, 0),
       coalesce(o.critical, 0),
       coalesce(o.warning, 0),
       coalesce(o.info, 0)
FROM asset_rollup a
LEFT JOIN open_alert_rollup o ON o.site_id = a.site_id`

func buildListSitesQuery(afterSiteID *string, limit int) (string, []any) {
	b := newPredicates()
	if afterSiteID != nil {
		b.add("a.site_id > ", *afterSiteID)
	}
	return b.finish(listSitesSQL, "ORDER BY a.site_id", limit)
}

func (s *Store) ListSites(ctx context.Context, query domain.SiteQuery) (page.Page[domain.Site], error) {
	var afterSiteID *string
	if query.Cursor != "" {
		fields, err := page.Decode(query.Cursor, 1)
		if err != nil {
			return page.Page[domain.Site]{}, err
		}
		afterSiteID = &fields[0]
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	sql, args := buildListSitesQuery(afterSiteID, query.Limit+1)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return page.Page[domain.Site]{}, fmt.Errorf("query sites: %w", err)
	}
	defer rows.Close()

	sites := make([]domain.Site, 0, query.Limit+1)
	for rows.Next() {
		var site domain.Site
		if err := rows.Scan(
			&site.SiteID,
			&site.AssetCount,
			&site.LastSeenAt,
			&site.OpenAlerts.Total,
			&site.OpenAlerts.Critical,
			&site.OpenAlerts.Warning,
			&site.OpenAlerts.Info,
		); err != nil {
			return page.Page[domain.Site]{}, fmt.Errorf("read sites: %w", err)
		}
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		return page.Page[domain.Site]{}, fmt.Errorf("read sites: %w", err)
	}

	return page.Build(sites, query.Limit, func(site domain.Site) string {
		return page.Encode(site.SiteID)
	}), nil
}
