// Tests SQL construction for paginated listings.
package postgres

import (
	"strings"
	"testing"
	"time"

	"api-go/internal/domain"
)

func TestBuildListAssetsQueryOnlyAddsPresentFilters(t *testing.T) {
	siteID := "rivers-bolo"
	assetType := domain.AssetTypeSmartMeter

	sql, args := buildListAssetsQuery(domain.AssetFilter{
		SiteID:    &siteID,
		AssetType: &assetType,
	}, nil, 51)

	if strings.Contains(sql, "IS NULL OR") {
		t.Fatalf("query still has nullable filter predicates: %s", sql)
	}
	for _, want := range []string{
		"WHERE a.site_id = $1",
		"AND a.asset_type = $2",
		"ORDER BY a.asset_id",
		"LIMIT $3",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("query missing %q: %s", want, sql)
		}
	}
	if len(args) != 3 || args[0] != siteID || args[1] != string(assetType) || args[2] != 51 {
		t.Fatalf("args = %#v, want site, asset type, limit", args)
	}
}

func TestBuildListAlertsQueryKeepsOpenStatusLiteral(t *testing.T) {
	status := domain.AlertStatusOpen
	severity := domain.SeverityCritical
	afterOpenedAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	afterAlertID := "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b"

	sql, args := buildListAlertsQuery(domain.AlertFilter{
		Status:   &status,
		Severity: &severity,
	}, &afterOpenedAt, &afterAlertID, 51)

	if strings.Contains(sql, "IS NULL OR") {
		t.Fatalf("query still has nullable filter predicates: %s", sql)
	}
	for _, want := range []string{
		"WHERE status = 'open'",
		"AND severity = $1",
		"AND (opened_at, alert_id) < ($2::timestamptz, $3::uuid)",
		"ORDER BY opened_at DESC, alert_id DESC",
		"LIMIT $4",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("query missing %q: %s", want, sql)
		}
	}
	if len(args) != 4 || args[0] != string(severity) || args[1] != afterOpenedAt || args[2] != afterAlertID || args[3] != 51 {
		t.Fatalf("args = %#v, want severity, cursor, limit", args)
	}
}

func TestBuildListSitesQueryAddsOnlyTheCursorPredicateWhenPresent(t *testing.T) {
	afterSiteID := "site-01"

	sql, args := buildListSitesQuery(&afterSiteID, 51)

	for _, want := range []string{
		"WHERE a.site_id > $1",
		"ORDER BY a.site_id",
		"LIMIT $2",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("query missing %q: %s", want, sql)
		}
	}
	if len(args) != 2 || args[0] != afterSiteID || args[1] != 51 {
		t.Fatalf("args = %#v, want cursor and limit", args)
	}
}
