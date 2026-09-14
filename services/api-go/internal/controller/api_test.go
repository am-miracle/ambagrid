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
	"strings"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/page"
	"api-go/internal/services"
)

type fakeAssets struct {
	listResult         page.Page[domain.Asset]
	listErr            error
	asset              domain.Asset
	getErr             error
	series             domain.ReadingSeries
	readingsErr        error
	gotRequest         services.ListAssetsRequest
	gotReadingsRequest services.GetReadingSeriesRequest
}

func (f *fakeAssets) List(_ context.Context, request services.ListAssetsRequest) (page.Page[domain.Asset], error) {
	f.gotRequest = request
	return f.listResult, f.listErr
}

func (f *fakeAssets) Get(context.Context, string) (domain.Asset, error) {
	return f.asset, f.getErr
}

func (f *fakeAssets) Readings(_ context.Context, _ string, request services.GetReadingSeriesRequest) (domain.ReadingSeries, error) {
	f.gotReadingsRequest = request
	return f.series, f.readingsErr
}

type fakeAlerts struct {
	listResult        page.Page[domain.Alert]
	listErr           error
	detail            services.AlertDetail
	getErr            error
	resolved          domain.Alert
	resolveErr        error
	gotRequest        services.ListAlertsRequest
	gotResolveAlertID string
	gotResolveRequest services.ResolveAlertRequest
}

func (f *fakeAlerts) List(_ context.Context, request services.ListAlertsRequest) (page.Page[domain.Alert], error) {
	f.gotRequest = request
	return f.listResult, f.listErr
}

func (f *fakeAlerts) Get(context.Context, string) (services.AlertDetail, error) {
	return f.detail, f.getErr
}

func (f *fakeAlerts) Resolve(_ context.Context, alertID string, request services.ResolveAlertRequest) (domain.Alert, error) {
	f.gotResolveAlertID = alertID
	f.gotResolveRequest = request
	return f.resolved, f.resolveErr
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

func (a testAPI) post(t *testing.T, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	a.handler.ServeHTTP(recorder, request)
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

func TestGetAssetReadingsReturnsChartReadyPoints(t *testing.T) {
	api := newTestAPI()
	from := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	api.assets.series = domain.ReadingSeries{
		AssetID:     "met-0104",
		AssetType:   domain.AssetTypeSmartMeter,
		From:        from,
		To:          from.Add(time.Hour),
		Metric:      domain.ReadingMetricVoltage,
		Interval:    domain.ReadingIntervalFiveMinutes,
		Aggregation: domain.ReadingAggregationAverage,
		Points: []domain.ReadingPoint{{
			Time:  from,
			Value: 231.2,
		}},
	}

	body := decodeBody(t, api.get(t, "/v1/assets/met-0104/readings?from=2026-09-14T10:00:00Z&to=2026-09-14T11:00:00Z&metric=voltage&interval=5m"))
	data := body["data"].(map[string]any)
	if data["metric"] != "voltage" || data["interval"] != "5m" || data["aggregation"] != "avg" {
		t.Fatalf("series metadata = %v", data)
	}
	points := data["points"].([]any)
	if len(points) != 1 || points[0].(map[string]any)["value"] != 231.2 {
		t.Fatalf("points = %v", points)
	}
	request := api.assets.gotReadingsRequest
	if request.From != from || request.To != from.Add(time.Hour) || request.Metric != domain.ReadingMetricVoltage || request.Interval != domain.ReadingIntervalFiveMinutes {
		t.Fatalf("request = %+v", request)
	}
}

func TestGetAssetReadingsEncodesNoPointsAsAnArray(t *testing.T) {
	api := newTestAPI()
	from := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	api.assets.series = domain.ReadingSeries{
		AssetID:     "met-0104",
		AssetType:   domain.AssetTypeSmartMeter,
		From:        from,
		To:          from.Add(time.Hour),
		Metric:      domain.ReadingMetricVoltage,
		Interval:    domain.ReadingIntervalFiveMinutes,
		Aggregation: domain.ReadingAggregationAverage,
	}

	body := decodeBody(t, api.get(t, "/v1/assets/met-0104/readings?from=2026-09-14T10:00:00Z&to=2026-09-14T11:00:00Z&metric=voltage&interval=5m"))
	points, ok := body["data"].(map[string]any)["points"].([]any)
	if !ok || len(points) != 0 {
		t.Fatalf("points = %v, want []", body["data"].(map[string]any)["points"])
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

func TestResolveAlertUsesTheTrustedOperatorHeader(t *testing.T) {
	api := newTestAPI()
	resolvedAt := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	note := "fan cleaned"
	resolvedBy := "operator-0101"
	api.alerts.resolved = domain.Alert{
		AlertID:        "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b",
		Severity:       domain.SeverityCritical,
		Status:         domain.AlertStatusResolved,
		ResolvedAt:     &resolvedAt,
		ResolutionNote: &note,
		ResolvedBy:     &resolvedBy,
	}

	recorder := api.post(t, "/v1/alerts/0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b/resolve", `{"resolution_note":"fan cleaned"}`, map[string]string{
		operatorIDHeader: "operator-0101",
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if api.alerts.gotResolveAlertID != "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b" {
		t.Fatalf("alert id = %q", api.alerts.gotResolveAlertID)
	}
	if got := api.alerts.gotResolveRequest; got.ResolutionNote != "fan cleaned" || got.ResolvedBy != "operator-0101" {
		t.Fatalf("resolve request = %+v", got)
	}
	data := decodeBody(t, recorder)["data"].(map[string]any)
	if data["status"] != "resolved" || data["resolved_by"] != "operator-0101" || data["resolution_note"] != "fan cleaned" {
		t.Fatalf("data = %v", data)
	}
}

func TestResolveAlertRequiresTrustedOperatorIdentity(t *testing.T) {
	api := newTestAPI()

	recorder := api.post(t, "/v1/alerts/0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b/resolve", `{"resolution_note":"fan cleaned"}`, nil)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if api.alerts.gotResolveAlertID != "" {
		t.Fatalf("service was called for alert %q", api.alerts.gotResolveAlertID)
	}
	if code := decodeBody(t, recorder)["error"].(map[string]any)["code"]; code != codeUnauthenticated {
		t.Fatalf("code = %v, want %v", code, codeUnauthenticated)
	}
}

func TestResolveAlertRejectsAnEmptyTrustedOperatorIdentity(t *testing.T) {
	api := newTestAPI()
	api.alerts.resolveErr = domain.ErrInvalidID

	recorder := api.post(t, "/v1/alerts/0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b/resolve", `{"resolution_note":"fan cleaned"}`, map[string]string{
		operatorIDHeader: "",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if api.alerts.gotResolveAlertID == "" || api.alerts.gotResolveRequest.ResolvedBy != "" {
		t.Fatalf("resolve request = id %q, request %+v", api.alerts.gotResolveAlertID, api.alerts.gotResolveRequest)
	}
}

func TestResolveAlertRejectsInvalidJSON(t *testing.T) {
	api := newTestAPI()

	recorder := api.post(t, "/v1/alerts/0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b/resolve", `{"resolution_note":`, map[string]string{
		operatorIDHeader: "operator-0101",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestErrorsMapToStatusCodes(t *testing.T) {
	tests := map[string]struct {
		target     string
		serviceErr error
		wantStatus int
		wantCode   string
	}{
		"missing object":   {target: "/v1/assets/met-0104", serviceErr: domain.ErrNotFound, wantStatus: http.StatusNotFound, wantCode: codeNotFound},
		"malformed id":     {target: "/v1/assets/met-0104", serviceErr: domain.ErrInvalidID, wantStatus: http.StatusBadRequest, wantCode: codeInvalidArgument},
		"command conflict": {target: "/v1/assets/met-0104", serviceErr: domain.ErrConflict, wantStatus: http.StatusConflict, wantCode: codeConflict},
		"forged cursor":    {target: "/v1/assets/met-0104", serviceErr: page.ErrInvalidCursor, wantStatus: http.StatusBadRequest, wantCode: codeInvalidArgument},
		"oversized page":   {target: "/v1/assets/met-0104", serviceErr: services.ErrInvalidRequest, wantStatus: http.StatusBadRequest, wantCode: codeInvalidArgument},
		"slow query":       {target: "/v1/assets/met-0104", serviceErr: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantCode: codeDeadlineExceeded},
		"database is out":  {target: "/v1/assets/met-0104", serviceErr: errors.New("connection refused"), wantStatus: http.StatusInternalServerError, wantCode: codeInternal},
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
		"unknown asset type":                  "/v1/assets?asset_type=hydro_turbine",
		"unknown severity":                    "/v1/alerts?severity=catastrophic",
		"unknown status":                      "/v1/alerts?status=acknowledged",
		"limit is words":                      "/v1/assets?limit=all",
		"limit is zero":                       "/v1/assets?limit=0",
		"readings needs from":                 "/v1/assets/met-0104/readings?to=2026-09-14T11:00:00Z&metric=voltage&interval=5m",
		"readings needs a valid timestamp":    "/v1/assets/met-0104/readings?from=yesterday&to=2026-09-14T11:00:00Z&metric=voltage&interval=5m",
		"readings needs a metric":             "/v1/assets/met-0104/readings?from=2026-09-14T10:00:00Z&to=2026-09-14T11:00:00Z&interval=5m",
		"readings rejects an unknown metric":  "/v1/assets/met-0104/readings?from=2026-09-14T10:00:00Z&to=2026-09-14T11:00:00Z&metric=phase_angle&interval=5m",
		"readings needs a supported interval": "/v1/assets/met-0104/readings?from=2026-09-14T10:00:00Z&to=2026-09-14T11:00:00Z&metric=voltage&interval=2m",
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
		t.Fatalf("status = %d, want 405 for an unrouted method", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, OPTIONS" {
		t.Fatalf("Allow = %q, want the supported methods", got)
	}
	if code := decodeBody(t, recorder)["error"].(map[string]any)["code"]; code != codeMethodNotAllowed {
		t.Fatalf("code = %v, want %v", code, codeMethodNotAllowed)
	}
}

func TestResolveRouteAdvertisesOnlyItsSupportedMethod(t *testing.T) {
	api := newTestAPI()
	recorder := httptest.NewRecorder()

	api.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/v1/alerts/0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b/resolve", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "POST, OPTIONS" {
		t.Fatalf("Allow = %q, want POST and OPTIONS", got)
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
