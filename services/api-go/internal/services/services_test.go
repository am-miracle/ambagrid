// Tests service rules with in-memory repositories.
package services

import (
	"context"
	"errors"
	"testing"

	"api-go/internal/domain"
	"api-go/internal/page"
)

type fakeAlertRepository struct {
	alert         domain.Alert
	getErr        error
	resolutions   []domain.AlertResolution
	gotAlertQuery domain.AlertQuery
	detailCalls   int
}

func (f *fakeAlertRepository) ListAlerts(_ context.Context, query domain.AlertQuery) (page.Page[domain.Alert], error) {
	f.gotAlertQuery = query
	return page.Page[domain.Alert]{Limit: query.Limit}, nil
}

func (f *fakeAlertRepository) GetAlertWithResolutions(context.Context, string) (domain.Alert, []domain.AlertResolution, error) {
	f.detailCalls++
	return f.alert, f.resolutions, f.getErr
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
		resolutions: []domain.AlertResolution{{ResolvedBy: "operator-0101"}},
	}

	detail, err := newTestAlertService(repo).Get(context.Background(), "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if len(detail.Resolutions) != 1 || detail.Resolutions[0].ResolvedBy != "operator-0101" {
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

type fakeAssetRepository struct {
	gotQuery domain.AssetQuery
	calls    int
}

func (f *fakeAssetRepository) ListAssets(_ context.Context, query domain.AssetQuery) (page.Page[domain.Asset], error) {
	f.gotQuery = query
	return page.Page[domain.Asset]{Limit: query.Limit}, nil
}

func (f *fakeAssetRepository) GetAsset(context.Context, string) (domain.Asset, error) {
	f.calls++
	return domain.Asset{}, nil
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
