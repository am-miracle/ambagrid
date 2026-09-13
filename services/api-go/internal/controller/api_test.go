// Tests the API through its complete HTTP stack.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
	"api-go/internal/services"
)

type fakeAssets struct {
	listResult page.Page[domain.Asset]
	listErr    error
	asset      domain.Asset
	getErr     error
	gotRequest services.ListAssetsRequest
}

func (f *fakeAssets) List(_ context.Context, request services.ListAssetsRequest) (page.Page[domain.Asset], error) {
	f.gotRequest = request
	return f.listResult, f.listErr
}

func (f *fakeAssets) Get(context.Context, string) (domain.Asset, error) {
	return f.asset, f.getErr
}

type fakeAlerts struct {
	listResult page.Page[domain.Alert]
	listErr    error
	detail     services.AlertDetail
	getErr     error
	gotRequest services.ListAlertsRequest
}

func (f *fakeAlerts) List(_ context.Context, request services.ListAlertsRequest) (page.Page[domain.Alert], error) {
	f.gotRequest = request
	return f.listResult, f.listErr
}

func (f *fakeAlerts) Get(context.Context, string) (services.AlertDetail, error) {
	return f.detail, f.getErr
}

type fakeSites struct {
	listResult page.Page[domain.Site]
	listErr    error
}

func (f *fakeSites) List(context.Context, services.ListSitesRequest) (page.Page[domain.Site], error) {
	return f.listResult, f.listErr
}

type fakeHealth struct{ err error }

func (f *fakeHealth) Ready(context.Context) error { return f.err }

type testAPI struct {
	handler http.Handler
	assets  *fakeAssets
	alerts  *fakeAlerts
	sites   *fakeSites
	health  *fakeHealth
}

func newTestAPI() testAPI {
	assets := &fakeAssets{}
	alerts := &fakeAlerts{}
	sites := &fakeSites{}
	health := &fakeHealth{}

	api := API{
		Assets:         assets,
		Alerts:         alerts,
		Sites:          sites,
		Health:         health,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		RequestTimeout: time.Second,
		AllowedOrigins: []string{"http://localhost:5173"},
	}

	return testAPI{handler: api.Handler(), assets: assets, alerts: alerts, sites: sites, health: health}
}

func (a testAPI) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	a.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", recorder.Body.String(), err)
	}
	return body
}

func TestListAssetsReturnsAnEnvelopeWithPageMetadata(t *testing.T) {
	api := newTestAPI()
	voltage := float32(230.5)
	api.assets.listResult = page.Page[domain.Asset]{
		Items: []domain.Asset{{
			AssetID:    "met-0104",
			SiteID:     "site-01",
			AssetType:  domain.AssetTypeSmartMeter,
			LastSeenAt: time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
			SmartMeter: &domain.SmartMeterState{Voltage: &voltage},
		}},
		Limit:      50,
		NextCursor: "cursor-token",
	}

	recorder := api.get(t, "/v1/assets")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decodeBody(t, recorder)
	data, ok := body["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("data = %v, want one asset", body["data"])
	}
	asset := data[0].(map[string]any)
	if asset["asset_id"] != "met-0104" {
		t.Fatalf("asset_id = %v", asset["asset_id"])
	}
	meter, ok := asset["smart_meter"].(map[string]any)
	if !ok || meter["voltage"] != 230.5 {
		t.Fatalf("smart_meter = %v, want the meter state block", asset["smart_meter"])
	}
	// Only the matching type-specific state block may appear.
	if _, present := asset["battery_bms"]; present {
		t.Fatalf("battery_bms present on a smart meter: %v", asset)
	}
	meta := body["page"].(map[string]any)
	if meta["next_cursor"] != "cursor-token" || meta["limit"] != float64(50) {
		t.Fatalf("page = %v, want the service's cursor and limit", meta)
	}
	if body["request_id"] == "" || body["request_id"] != recorder.Header().Get(requestIDHeader) {
		t.Fatalf("request_id = %v, header = %q", body["request_id"], recorder.Header().Get(requestIDHeader))
	}
}

func TestListAssetsEncodesAnEmptyPageAsAnArray(t *testing.T) {
	api := newTestAPI()

	body := decodeBody(t, api.get(t, "/v1/assets"))

	data, ok := body["data"].([]any)
	if !ok || len(data) != 0 {
		t.Fatalf("data = %v, want []", body["data"])
	}
	if meta := body["page"].(map[string]any); meta["next_cursor"] != "" {
		t.Fatalf("next_cursor = %v, want an empty exhaustion marker", meta["next_cursor"])
	}
}

func TestListAssetsPassesFiltersAndPagingToTheService(t *testing.T) {
	api := newTestAPI()

	api.get(t, "/v1/assets?site_id=site-01&asset_type=battery_bms&limit=10&cursor=abc")

	request := api.assets.gotRequest
	if request.Limit != 10 || request.Cursor != "abc" {
		t.Fatalf("request = %+v, want limit 10 and cursor abc", request)
	}
	if request.Filter.SiteID == nil || *request.Filter.SiteID != "site-01" {
		t.Fatalf("site_id filter = %v", request.Filter.SiteID)
	}
	if request.Filter.AssetType == nil || *request.Filter.AssetType != domain.AssetTypeBatteryBMS {
		t.Fatalf("asset_type filter = %v", request.Filter.AssetType)
	}
}

func TestGetAlertIncludesResolutionHistory(t *testing.T) {
	api := newTestAPI()
	api.alerts.detail = services.AlertDetail{
		Alert: domain.Alert{
			AlertID:  "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b",
			Severity: domain.SeverityCritical,
			Status:   domain.AlertStatusResolved,
		},
		Resolutions: []domain.AlertResolution{{ResolvedBy: "operator-0101", ResolutionNote: "fan cleaned"}},
	}

	recorder := api.get(t, "/v1/alerts/0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b")
	body := decodeBody(t, recorder)

	data := body["data"].(map[string]any)
	if data["severity"] != "critical" || data["status"] != "resolved" {
		t.Fatalf("data = %v, want the alert's own fields promoted", data)
	}
	resolutions, ok := data["resolutions"].([]any)
	if !ok || len(resolutions) != 1 {
		t.Fatalf("resolutions = %v, want one entry", data["resolutions"])
	}
	if resolutions[0].(map[string]any)["resolved_by"] != "operator-0101" {
		t.Fatalf("resolution = %v", resolutions[0])
	}
	if body["request_id"] == "" || body["request_id"] != recorder.Header().Get(requestIDHeader) {
		t.Fatalf("request_id = %v, header = %q", body["request_id"], recorder.Header().Get(requestIDHeader))
	}
}

func TestErrorsMapToStatusCodes(t *testing.T) {
	tests := map[string]struct {
		target     string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		"missing object":  {target: "/v1/assets/met-0104", serviceErr: domain.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: codeNotFound},
		"malformed id":    {target: "/v1/assets/met-0104", serviceErr: domain.ErrInvalidID, wantStatus: http.StatusBadRequest, wantCode: codeInvalidArgument},
		"forged cursor":   {target: "/v1/assets/met-0104", serviceErr: page.ErrInvalidCursor, wantStatus: http.StatusBadRequest, wantCode: codeInvalidArgument},
		"oversized page":  {target: "/v1/assets/met-0104", serviceErr: services.ErrInvalidRequest, wantStatus: http.StatusBadRequest, wantCode: codeInvalidArgument},
		"slow query":      {target: "/v1/assets/met-0104", serviceErr: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantCode: codeDeadlineExceeded},
		"database is out": {target: "/v1/assets/met-0104", serviceErr: errors.New("connection refused"), wantStatus: http.StatusInternalServerError, wantCode: codeInternal},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			api := newTestAPI()
			api.assets.getErr = test.serviceErr

			recorder := api.get(t, test.target)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			detail := decodeBody(t, recorder)["error"].(map[string]any)
			if detail["code"] != test.wantCode {
				t.Fatalf("code = %v, want %v", detail["code"], test.wantCode)
			}
			if detail["request_id"] == "" || detail["request_id"] == nil {
				t.Fatalf("error body carries no request_id: %v", detail)
			}
		})
	}
}

func TestAnInternalErrorDoesNotLeakItsCause(t *testing.T) {
	api := newTestAPI()
	api.assets.getErr = errors.New("pq: relation \"assets\" does not exist")

	body := decodeBody(t, api.get(t, "/v1/assets/met-0104"))

	if message := body["error"].(map[string]any)["message"]; message != "internal error" {
		t.Fatalf("message = %v, want the cause kept in the logs", message)
	}
}

func TestInvalidQueryParametersAreRejectedBeforeTheService(t *testing.T) {
	tests := map[string]string{
		"unknown asset type": "/v1/assets?asset_type=hydro_turbine",
		"unknown severity":   "/v1/alerts?severity=catastrophic",
		"unknown status":     "/v1/alerts?status=acknowledged",
		"limit is words":     "/v1/assets?limit=all",
		"limit is zero":      "/v1/assets?limit=0",
	}

	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			api := newTestAPI()

			recorder := api.get(t, target)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
		})
	}
}

func TestUnknownRoutesAndMethodsStayInTheJSONEnvelope(t *testing.T) {
	api := newTestAPI()

	recorder := api.get(t, "/v1/customers")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want JSON", got)
	}
}

func TestWriteMethodsAreNotRouted(t *testing.T) {
	api := newTestAPI()
	recorder := httptest.NewRecorder()

	api.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/alerts", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 on a read-only surface", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, OPTIONS" {
		t.Fatalf("Allow = %q, want the readable methods", got)
	}
	if code := decodeBody(t, recorder)["error"].(map[string]any)["code"]; code != codeMethodNotAllowed {
		t.Fatalf("code = %v, want %v", code, codeMethodNotAllowed)
	}
}

func TestReadinessFollowsTheDatabase(t *testing.T) {
	api := newTestAPI()

	if recorder := api.get(t, "/readyz"); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 while the database is reachable", recorder.Code)
	}

	api.health.err = errors.New("connection refused")

	if recorder := api.get(t, "/readyz"); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 once it is not", recorder.Code)
	}
	// Liveness remains healthy during a database outage.
	if recorder := api.get(t, "/healthz"); recorder.Code != http.StatusOK {
		t.Fatalf("liveness status = %d, want 200 during a database outage", recorder.Code)
	}
}
