// Tests service rules with in-memory repositories.
package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
)

type fakeAlertRepository struct {
	alert             domain.Alert
	getErr            error
	resolveErr        error
	resolutions       []domain.AlertResolution
	gotAlertQuery     domain.AlertQuery
	gotResolveCommand domain.ResolveAlertCommand
	detailCalls       int
	resolveCalls      int
}

func (f *fakeAlertRepository) ListAlerts(_ context.Context, query domain.AlertQuery) (page.Page[domain.Alert], error) {
	f.gotAlertQuery = query
	return page.Page[domain.Alert]{Limit: query.Limit}, nil
}

func (f *fakeAlertRepository) GetAlertWithResolutions(context.Context, string) (domain.Alert, []domain.AlertResolution, error) {
	f.detailCalls++
	return f.alert, f.resolutions, f.getErr
}

func (f *fakeAlertRepository) ResolveAlert(_ context.Context, command domain.ResolveAlertCommand) (domain.Alert, error) {
	f.resolveCalls++
	f.gotResolveCommand = command
	return f.alert, f.resolveErr
}

func testLimits() PageLimits {
	return PageLimits{DefaultSize: 50, MaxSize: 200}
}

const testGlobalHistorySize = 25

func newTestAlertService(repo AlertRepository) *AlertService {
	return NewAlertService(repo, testLimits(), PageLimits{
		DefaultSize: testGlobalHistorySize,
		MaxSize:     testGlobalHistorySize,
	})
}

func TestPageLimitsResolve(t *testing.T) {
	tests := map[string]struct {
		requested int
		want      int
		wantErr   bool
	}{
		"unset takes the default": {requested: 0, want: 50},
		"in range is honored":     {requested: 10, want: 10},
		"at the ceiling":          {requested: 200, want: 200},
		"over the ceiling":        {requested: 201, wantErr: true},
		"negative":                {requested: -1, wantErr: true},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := testLimits().Resolve(test.requested)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("Resolve() error = %v, want ErrInvalidRequest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Resolve() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestAlertServiceListAppliesTheResolvedLimit(t *testing.T) {
	repo := &fakeAlertRepository{}

	if _, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{}); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if repo.gotAlertQuery.Limit != 50 {
		t.Fatalf("repository saw limit %d, want the default 50", repo.gotAlertQuery.Limit)
	}
}

func TestAlertServiceListDefaultsGlobalAlertsToOpen(t *testing.T) {
	repo := &fakeAlertRepository{}

	if _, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{}); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	status := repo.gotAlertQuery.Filter.Status
	if status == nil || *status != domain.AlertStatusOpen {
		t.Fatalf("status filter = %v, want open for an unscoped global list", status)
	}
}

func TestAlertServiceListCapsUnscopedHistory(t *testing.T) {
	repo := &fakeAlertRepository{}
	resolved := domain.AlertStatusResolved

	_, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{
		Filter: domain.AlertFilter{Status: &resolved},
		Limit:  testGlobalHistorySize + 1,
	})

	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("List() error = %v, want ErrInvalidRequest", err)
	}
}

func TestAlertServiceListDefaultsUnscopedHistoryToTheLowerLimit(t *testing.T) {
	repo := &fakeAlertRepository{}
	resolved := domain.AlertStatusResolved

	if _, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{
		Filter: domain.AlertFilter{Status: &resolved},
	}); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if repo.gotAlertQuery.Limit != testGlobalHistorySize {
		t.Fatalf("repository saw limit %d, want the global history cap", repo.gotAlertQuery.Limit)
	}
}

func TestAlertServiceListLetsScopedHistoryUseTheNormalLimit(t *testing.T) {
	repo := &fakeAlertRepository{}
	resolved := domain.AlertStatusResolved
	siteID := "site-01"

	if _, err := newTestAlertService(repo).List(context.Background(), ListAlertsRequest{
		Filter: domain.AlertFilter{SiteID: &siteID, Status: &resolved},
		Limit:  50,
	}); err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if repo.gotAlertQuery.Limit != 50 {
		t.Fatalf("repository saw limit %d, want 50 for scoped alert history", repo.gotAlertQuery.Limit)
	}
}

func TestAlertServiceGetReturnsResolutionHistoryWithTheAlert(t *testing.T) {
	repo := &fakeAlertRepository{
		alert:       domain.Alert{AlertID: "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b"},
		resolutions: []domain.AlertResolution{{ResolvedBy: "actor-0101"}},
	}

	detail, err := newTestAlertService(repo).Get(context.Background(), "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if len(detail.Resolutions) != 1 || detail.Resolutions[0].ResolvedBy != "actor-0101" {
		t.Fatalf("Resolutions = %v, want the repository's history", detail.Resolutions)
	}
}

func TestAlertServiceGetRejectsAMalformedIDBeforeQuerying(t *testing.T) {
	repo := &fakeAlertRepository{getErr: errors.New("should not be called")}

	_, err := newTestAlertService(repo).Get(context.Background(), "not-a-uuid")

	if !errors.Is(err, domain.ErrInvalidID) {
		t.Fatalf("Get() error = %v, want ErrInvalidID", err)
	}
	if repo.detailCalls != 0 {
		t.Fatalf("repository was queried %d times, want 0", repo.detailCalls)
	}
}

func TestAlertServiceGetReturnsMissingAlertError(t *testing.T) {
	repo := &fakeAlertRepository{getErr: domain.ErrNotFound}

	if _, err := newTestAlertService(repo).Get(context.Background(), "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}

	if repo.detailCalls != 1 {
		t.Fatalf("repository was queried %d times, want 1", repo.detailCalls)
	}
}

func TestAlertServiceResolveValidatesAndPassesACommandThrough(t *testing.T) {
	repo := &fakeAlertRepository{
		alert: domain.Alert{AlertID: "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b", Status: domain.AlertStatusResolved},
	}

	alert, err := newTestAlertService(repo).Resolve(context.Background(), "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b", ResolveAlertRequest{
		ResolutionNote: "  fan cleaned  ",
		ResolvedBy:     " actor-0101 ",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if alert.Status != domain.AlertStatusResolved {
		t.Fatalf("alert status = %v, want resolved", alert.Status)
	}
	if got := repo.gotResolveCommand; got.AlertID != "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b" || got.ResolutionNote != "fan cleaned" || got.ResolvedBy != "actor-0101" {
		t.Fatalf("resolve command = %+v", got)
	}
}

func TestAlertServiceResolveRejectsBadInputBeforeQuerying(t *testing.T) {
	tests := map[string]ResolveAlertRequest{
		"empty note":      {ResolutionNote: " ", ResolvedBy: "actor-0101"},
		"empty operator":  {ResolutionNote: "fan cleaned", ResolvedBy: " "},
		"system operator": {ResolutionNote: "fan cleaned", ResolvedBy: "system"},
	}

	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			repo := &fakeAlertRepository{}

			_, err := newTestAlertService(repo).Resolve(context.Background(), "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b", request)

			if err == nil {
				t.Fatal("Resolve() error = nil, want validation error")
			}
			if repo.resolveCalls != 0 {
				t.Fatalf("repository was queried %d times, want 0", repo.resolveCalls)
			}
		})
	}
}

type fakeAssetRepository struct {
	gotQuery     domain.AssetQuery
	calls        int
	asset        domain.Asset
	getErr       error
	readingQuery domain.ReadingQuery
	readingCalls int
}

func (f *fakeAssetRepository) ListAssets(_ context.Context, query domain.AssetQuery) (page.Page[domain.Asset], error) {
	f.gotQuery = query
	return page.Page[domain.Asset]{Limit: query.Limit}, nil
}

func (f *fakeAssetRepository) GetAsset(context.Context, string) (domain.Asset, error) {
	f.calls++
	return f.asset, f.getErr
}

func (f *fakeAssetRepository) GetReadingSeries(_ context.Context, query domain.ReadingQuery) (domain.ReadingSeries, error) {
	f.readingCalls++
	f.readingQuery = query
	return domain.ReadingSeries{}, nil
}

func TestAssetServiceGetRejectsAnEmptyID(t *testing.T) {
	repo := &fakeAssetRepository{}

	_, err := NewAssetService(repo, testLimits()).Get(context.Background(), "   ")

	if !errors.Is(err, domain.ErrInvalidID) {
		t.Fatalf("Get() error = %v, want ErrInvalidID", err)
	}
	if repo.calls != 0 {
		t.Fatalf("repository was queried %d times, want 0", repo.calls)
	}
}

func TestAssetServiceListPassesFiltersThrough(t *testing.T) {
	repo := &fakeAssetRepository{}
	siteID := "site-01"
	assetType := domain.AssetTypeBatteryBMS

	_, err := NewAssetService(repo, testLimits()).List(context.Background(), ListAssetsRequest{
		Filter: domain.AssetFilter{SiteID: &siteID, AssetType: &assetType},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if got := repo.gotQuery.Filter; got.SiteID == nil || *got.SiteID != siteID || got.AssetType == nil || *got.AssetType != assetType {
		t.Fatalf("repository saw filter %+v, want the requested one", got)
	}
}

func TestAssetServiceReadingsPassesABoundedSeriesQueryThrough(t *testing.T) {
	repo := &fakeAssetRepository{asset: domain.Asset{AssetType: domain.AssetTypeSmartMeter}}
	from := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	_, err := NewAssetService(repo, testLimits()).Readings(context.Background(), "met-0104", GetReadingSeriesRequest{
		From:     from,
		To:       to,
		Metric:   domain.ReadingMetricVoltage,
		Interval: domain.ReadingIntervalOneMinute,
	})
	if err != nil {
		t.Fatalf("Readings() error = %v", err)
	}
	if repo.readingCalls != 1 || repo.readingQuery.AssetID != "met-0104" || repo.readingQuery.AssetType != domain.AssetTypeSmartMeter || repo.readingQuery.From != from || repo.readingQuery.To != to {
		t.Fatalf("repository query = %+v", repo.readingQuery)
	}
}

func TestAssetServiceReadingsRejectsInvalidWindowsBeforeQuerying(t *testing.T) {
	from := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	tests := map[string]time.Time{
		"equal endpoints":    from,
		"reversed endpoints": from.Add(-time.Minute),
		"window over limit":  from.Add(24*time.Hour + time.Nanosecond),
	}

	for name, to := range tests {
		t.Run(name, func(t *testing.T) {
			repo := &fakeAssetRepository{}
			_, err := NewAssetService(repo, testLimits()).Readings(context.Background(), "met-0104", GetReadingSeriesRequest{
				From:     from,
				To:       to,
				Metric:   domain.ReadingMetricVoltage,
				Interval: domain.ReadingIntervalOneMinute,
			})
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Readings() error = %v, want ErrInvalidRequest", err)
			}
			if repo.calls != 0 || repo.readingCalls != 0 {
				t.Fatalf("repository calls = asset:%d readings:%d, want none", repo.calls, repo.readingCalls)
			}
		})
	}
}

func TestAssetServiceReadingsRejectsMetricOutsideTheAssetSchema(t *testing.T) {
	repo := &fakeAssetRepository{asset: domain.Asset{AssetType: domain.AssetTypeBatteryBMS}}
	from := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

	_, err := NewAssetService(repo, testLimits()).Readings(context.Background(), "bat-0104", GetReadingSeriesRequest{
		From:     from,
		To:       from.Add(time.Hour),
		Metric:   domain.ReadingMetricVoltage,
		Interval: domain.ReadingIntervalFiveMinutes,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Readings() error = %v, want ErrInvalidRequest", err)
	}
	if repo.calls != 1 || repo.readingCalls != 0 {
		t.Fatalf("repository calls = asset:%d readings:%d, want 1/0", repo.calls, repo.readingCalls)
	}
}
