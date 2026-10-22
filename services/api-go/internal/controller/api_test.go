// Tests the API through its complete HTTP stack.
package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
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
	"api-go/internal/webhook"
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

type fakePayments struct {
	devResult         domain.ApplyPaymentResult
	devErr            error
	webhookResult     domain.ApplyPaymentResult
	webhookErr        error
	gotDevRequest     services.ApplyDevPaymentRequest
	gotWebhookCommand domain.ApplyPaymentCommand
	devCalls          int
	webhookCalls      int
}

func (f *fakePayments) ApplyDevPayment(_ context.Context, request services.ApplyDevPaymentRequest) (domain.ApplyPaymentResult, error) {
	f.devCalls++
	f.gotDevRequest = request
	return f.devResult, f.devErr
}

func (f *fakePayments) ApplyWebhookPayment(_ context.Context, command domain.ApplyPaymentCommand) (domain.ApplyPaymentResult, error) {
	f.webhookCalls++
	f.gotWebhookCommand = command
	return f.webhookResult, f.webhookErr
}

func (f *fakePayments) ListCustomers(_ context.Context) ([]domain.CustomerSummary, error) {
	return nil, nil
}

func (f *fakePayments) ListMeterCommands(_ context.Context) ([]domain.MeterCommand, error) {
	return nil, nil
}

func (f *fakePayments) ListAuditEvents(_ context.Context, _ string) ([]domain.AuditEvent, error) {
	return nil, nil
}

type fakeHealth struct{ err error }

func (f *fakeHealth) Ready(context.Context) error { return f.err }

type testAPI struct {
	handler  http.Handler
	assets   *fakeAssets
	alerts   *fakeAlerts
	sites    *fakeSites
	payments *fakePayments
	health   *fakeHealth
}

func newTestAPI() testAPI {
	assets := &fakeAssets{}
	alerts := &fakeAlerts{}
	sites := &fakeSites{}
	payments := &fakePayments{}
	health := &fakeHealth{}

	api := API{
		Assets:         assets,
		Alerts:         alerts,
		Sites:          sites,
		Payments:       payments,
		Health:         health,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		RequestTimeout: time.Second,
		AllowedOrigins: []string{"http://localhost:5173"},
		DevMode:        true,
	}

	return testAPI{handler: api.Handler(), assets: assets, alerts: alerts, sites: sites, payments: payments, health: health}
}

func newTestAPIWithPaystack(secret string) testAPI {
	assets := &fakeAssets{}
	alerts := &fakeAlerts{}
	sites := &fakeSites{}
	payments := &fakePayments{}
	health := &fakeHealth{}

	paystack, _ := webhook.NewPaystack(secret)
	api := API{
		Assets:   assets,
		Alerts:   alerts,
		Sites:    sites,
		Payments: payments,
		Health:   health,
		Webhooks: []WebhookRoute{{
			Pattern:  "/v1/webhooks/paystack",
			Provider: paystack,
		}},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		RequestTimeout: time.Second,
		AllowedOrigins: []string{"http://localhost:5173"},
		DevMode:        true,
	}

	return testAPI{handler: api.Handler(), assets: assets, alerts: alerts, sites: sites, payments: payments, health: health}
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

func TestListSitesReturnsMetadataAndOperationalRollups(t *testing.T) {
	api := newTestAPI()
	country := "NG"
	region := "Nasarawa"
	gridOperatorID := "operator-0101"
	lat := 8.4917
	lng := 8.5153
	lastSeenAt := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	lastContactAt := lastSeenAt.Add(5 * time.Second)
	oldestPendingAt := lastSeenAt.Add(-time.Minute)
	api.sites.listResult = page.Page[domain.Site]{
		Items: []domain.Site{{
			SiteID:          "nasarawa-duduguru",
			Name:            "Duduguru Mini-grid",
			Country:         &country,
			Region:          &region,
			GridOperatorID:  &gridOperatorID,
			Lat:             &lat,
			Lng:             &lng,
			Status:          "active",
			AssetCount:      3,
			LastSeenAt:      &lastSeenAt,
			HealthStatus:    domain.SiteHealthDelayed,
			LastContactAt:   &lastContactAt,
			LastEventAt:     &lastSeenAt,
			QueueDepth:      7,
			OldestPendingAt: &oldestPendingAt,
			QueueGrowing:    true,
			OpenAlerts:      domain.AlertCounts{Total: 1, Critical: 1},
		}},
		Limit: 50,
	}

	body := decodeBody(t, api.get(t, "/v1/sites"))
	site := body["data"].([]any)[0].(map[string]any)

	if site["name"] != "Duduguru Mini-grid" || site["country"] != "NG" || site["region"] != "Nasarawa" {
		t.Fatalf("site metadata = %v", site)
	}
	if site["operator_id"] != "operator-0101" || site["lat"] != lat || site["lng"] != lng || site["status"] != "active" {
		t.Fatalf("site operational metadata = %v", site)
	}
	if site["asset_count"] != float64(3) || site["open_alerts"].(map[string]any)["critical"] != float64(1) {
		t.Fatalf("site rollups = %v", site)
	}
	if site["health_status"] != "delayed" || site["queue_depth"] != float64(7) || site["queue_growing"] != true {
		t.Fatalf("site health = %v", site)
	}
	if site["last_contact_at"] == nil || site["last_event_timestamp"] == nil || site["oldest_pending_record_age_seconds"] == nil {
		t.Fatalf("site health timestamps = %v", site)
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
		Resolutions: []domain.AlertResolution{{ResolvedBy: "actor-0101", ResolutionNote: "fan cleaned"}},
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
	if resolutions[0].(map[string]any)["resolved_by"] != "actor-0101" {
		t.Fatalf("resolution = %v", resolutions[0])
	}
	if body["request_id"] == "" || body["request_id"] != recorder.Header().Get(requestIDHeader) {
		t.Fatalf("request_id = %v, header = %q", body["request_id"], recorder.Header().Get(requestIDHeader))
	}
}

func TestResolveAlertUsesTheTrustedActorHeader(t *testing.T) {
	api := newTestAPI()
	resolvedAt := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	note := "fan cleaned"
	resolvedBy := "actor-0101"
	api.alerts.resolved = domain.Alert{
		AlertID:        "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b",
		Severity:       domain.SeverityCritical,
		Status:         domain.AlertStatusResolved,
		ResolvedAt:     &resolvedAt,
		ResolutionNote: &note,
		ResolvedBy:     &resolvedBy,
	}

	recorder := api.post(t, "/v1/alerts/0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b/resolve", `{"resolution_note":"fan cleaned"}`, map[string]string{
		actorIDHeader: "actor-0101",
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if api.alerts.gotResolveAlertID != "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b" {
		t.Fatalf("alert id = %q", api.alerts.gotResolveAlertID)
	}
	if got := api.alerts.gotResolveRequest; got.ResolutionNote != "fan cleaned" || got.ResolvedBy != "actor-0101" {
		t.Fatalf("resolve request = %+v", got)
	}
	data := decodeBody(t, recorder)["data"].(map[string]any)
	if data["status"] != "resolved" || data["resolved_by"] != "actor-0101" || data["resolution_note"] != "fan cleaned" {
		t.Fatalf("data = %v", data)
	}
}

func TestResolveAlertRequiresTrustedActorIdentity(t *testing.T) {
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

func TestResolveAlertRejectsAnEmptyTrustedActorIdentity(t *testing.T) {
	api := newTestAPI()
	api.alerts.resolveErr = domain.ErrInvalidID

	recorder := api.post(t, "/v1/alerts/0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b/resolve", `{"resolution_note":"fan cleaned"}`, map[string]string{
		actorIDHeader: "",
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
		actorIDHeader: "actor-0101",
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

func samplePaymentResult() domain.ApplyPaymentResult {
	confirmedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	creditCreatedAt := confirmedAt
	balanceUpdatedAt := confirmedAt
	paymentID := "pay-001"
	tariffID := "tariff-01"
	moneyValue := int64(500000)
	return domain.ApplyPaymentResult{
		Payment: domain.Payment{
			PaymentID:         "pay-001",
			Provider:          "dev",
			ExternalReference: "dev-abc123",
			CustomerID:        "cust-01",
			AmountMinorUnits:  500000,
			Currency:          "NGN",
			Status:            domain.PaymentStatusConfirmed,
			ConfirmedAt:       &confirmedAt,
			CreatedAt:         confirmedAt,
		},
		Credit: domain.EnergyCredit{
			CreditID:             "credit-001",
			SiteID:               "site-01",
			AssignmentID:         "assign-001",
			PaymentID:            &paymentID,
			TariffPlanID:         &tariffID,
			SourceType:           domain.CreditSourcePayment,
			SourceID:             "pay-001",
			KWhGranted:           20.0,
			MoneyValueMinorUnits: &moneyValue,
			CreatedAt:            creditCreatedAt,
		},
		Balance: domain.CreditBalance{
			AssignmentID:                  "assign-001",
			RemainingKWh:                  20.0,
			RemainingMoneyValueMinorUnits: 500000,
			UpdatedAt:                     balanceUpdatedAt,
		},
		MeterCommand: &domain.MeterCommand{
			CommandID:   "cmd-001",
			MeterID:     "met-0101",
			CommandType: domain.CommandReconnectMeter,
			Status:      domain.CommandStatusRequested,
			RequestedBy: "provider:dev",
			Reason:      "balance recharged from zero",
			RequestedAt: confirmedAt,
		},
	}
}

func TestDevPaymentReturnsTheFullChainOn201(t *testing.T) {
	api := newTestAPI()
	api.payments.devResult = samplePaymentResult()

	recorder := api.post(t, "/v1/dev/payments", `{"customer_id":"cust-01","amount_minor_units":500000,"currency":"NGN"}`, nil)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeBody(t, recorder)
	data := body["data"].(map[string]any)

	payment := data["payment"].(map[string]any)
	if payment["payment_id"] != "pay-001" || payment["provider"] != "dev" || payment["status"] != "confirmed" {
		t.Fatalf("payment = %v", payment)
	}
	credit := data["credit"].(map[string]any)
	if credit["kwh_granted"] != 20.0 || credit["source_type"] != "payment" {
		t.Fatalf("credit = %v", credit)
	}
	balance := data["balance"].(map[string]any)
	if balance["remaining_kwh"] != 20.0 {
		t.Fatalf("balance = %v", balance)
	}
	cmd := data["meter_command"].(map[string]any)
	if cmd["command_type"] != "reconnect_meter" || cmd["status"] != "requested" {
		t.Fatalf("meter_command = %v", cmd)
	}

	if api.payments.devCalls != 1 {
		t.Fatalf("service calls = %d, want 1", api.payments.devCalls)
	}
	got := api.payments.gotDevRequest
	if got.CustomerID != "cust-01" || got.AmountMinorUnits != 500000 || got.Currency != "NGN" {
		t.Fatalf("service request = %+v", got)
	}
	if body["request_id"] == "" {
		t.Fatal("response missing request_id")
	}
}

func TestDevPaymentOmitsMeterCommandWhenNoneIssued(t *testing.T) {
	api := newTestAPI()
	result := samplePaymentResult()
	result.MeterCommand = nil
	api.payments.devResult = result

	recorder := api.post(t, "/v1/dev/payments", `{"customer_id":"cust-01","amount_minor_units":500000,"currency":"NGN"}`, nil)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", recorder.Code)
	}
	data := decodeBody(t, recorder)["data"].(map[string]any)
	if data["meter_command"] != nil {
		t.Fatalf("meter_command = %v, want nil", data["meter_command"])
	}
}

func TestDevPaymentRejectsInvalidJSON(t *testing.T) {
	api := newTestAPI()

	recorder := api.post(t, "/v1/dev/payments", `{"customer_id":`, nil)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if api.payments.devCalls != 0 {
		t.Fatalf("service was called %d times, want 0", api.payments.devCalls)
	}
}

func TestDevPaymentRejectsExtraJSONFields(t *testing.T) {
	api := newTestAPI()

	recorder := api.post(t, "/v1/dev/payments", `{"customer_id":"cust-01","amount_minor_units":500000,"currency":"NGN","extra":"field"}`, nil)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestDevPaymentMapsServiceErrorsToHTTPStatus(t *testing.T) {
	tests := map[string]struct {
		err        error
		wantStatus int
	}{
		"not found":   {err: domain.ErrNotFound, wantStatus: http.StatusNotFound},
		"conflict":    {err: domain.ErrConflict, wantStatus: http.StatusConflict},
		"bad request": {err: services.ErrInvalidRequest, wantStatus: http.StatusBadRequest},
		"internal":    {err: errors.New("database error"), wantStatus: http.StatusInternalServerError},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			api := newTestAPI()
			api.payments.devErr = test.err

			recorder := api.post(t, "/v1/dev/payments", `{"customer_id":"cust-01","amount_minor_units":500000,"currency":"NGN"}`, nil)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestDevPaymentReturns404WhenDevModeDisabled(t *testing.T) {
	api := newTestAPI()
	api.handler = (API{
		Assets:         api.assets,
		Alerts:         api.alerts,
		Sites:          api.sites,
		Payments:       api.payments,
		Health:         api.health,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		RequestTimeout: time.Second,
		AllowedOrigins: []string{"http://localhost:5173"},
		DevMode:        false,
	}).Handler()

	recorder := api.post(t, "/v1/dev/payments", `{"customer_id":"cust-01","amount_minor_units":500000,"currency":"NGN"}`, nil)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when dev mode is disabled", recorder.Code)
	}
	if api.payments.devCalls != 0 {
		t.Fatalf("service was called %d times, want 0", api.payments.devCalls)
	}
}

const testPaystackSecret = "sk_test_xxxxxxxxxxxxxxxxxxxxx"

func paystackSign(body []byte) string {
	mac := hmac.New(sha512.New, []byte(testPaystackSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func paystackChargeBody(reference, customerID, currency string, amount int64) string {
	envelope := map[string]any{
		"event": "charge.success",
		"data": map[string]any{
			"reference": reference,
			"amount":    amount,
			"currency":  currency,
			"status":    "success",
			"paid_at":   "2026-10-01T14:30:00Z",
			"metadata":  map[string]any{"customer_id": customerID},
		},
	}
	b, _ := json.Marshal(envelope)
	return string(b)
}

func TestWebhookPaystackAcceptsValidSignedPayload(t *testing.T) {
	api := newTestAPIWithPaystack(testPaystackSecret)
	api.payments.webhookResult = samplePaymentResult()

	body := paystackChargeBody("txn-abc-123", "cust-01", "NGN", 500000)
	sig := paystackSign([]byte(body))

	recorder := api.post(t, "/v1/webhooks/paystack", body, map[string]string{
		"X-Paystack-Signature": sig,
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if api.payments.webhookCalls != 1 {
		t.Fatalf("service calls = %d, want 1", api.payments.webhookCalls)
	}
	cmd := api.payments.gotWebhookCommand
	if cmd.Provider != "paystack" || cmd.ExternalReference != "txn-abc-123" || cmd.CustomerID != "cust-01" || cmd.AmountMinorUnits != 500000 || cmd.Currency != "NGN" {
		t.Fatalf("command = %+v", cmd)
	}

	data := decodeBody(t, recorder)["data"].(map[string]any)
	if data["payment"].(map[string]any)["payment_id"] != "pay-001" {
		t.Fatalf("response missing payment data")
	}
}

func TestWebhookPaystackRejectsMissingSignature(t *testing.T) {
	api := newTestAPIWithPaystack(testPaystackSecret)

	body := paystackChargeBody("txn-abc-123", "cust-01", "NGN", 500000)
	recorder := api.post(t, "/v1/webhooks/paystack", body, nil)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if api.payments.webhookCalls != 0 {
		t.Fatalf("service was called %d times, want 0", api.payments.webhookCalls)
	}
	if code := decodeBody(t, recorder)["error"].(map[string]any)["code"]; code != codeUnauthenticated {
		t.Fatalf("code = %v, want %v", code, codeUnauthenticated)
	}
}

func TestWebhookPaystackRejectsInvalidSignature(t *testing.T) {
	api := newTestAPIWithPaystack(testPaystackSecret)

	body := paystackChargeBody("txn-abc-123", "cust-01", "NGN", 500000)
	recorder := api.post(t, "/v1/webhooks/paystack", body, map[string]string{
		"X-Paystack-Signature": "deadbeef",
	})

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if api.payments.webhookCalls != 0 {
		t.Fatalf("service was called %d times, want 0", api.payments.webhookCalls)
	}
}

func TestWebhookPaystackRejectsMalformedPayload(t *testing.T) {
	api := newTestAPIWithPaystack(testPaystackSecret)

	body := []byte(`{"event":"transfer.success","data":{}}`)
	sig := paystackSign(body)

	recorder := api.post(t, "/v1/webhooks/paystack", string(body), map[string]string{
		"X-Paystack-Signature": sig,
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if api.payments.webhookCalls != 0 {
		t.Fatalf("service was called %d times, want 0", api.payments.webhookCalls)
	}
}

func TestWebhookPaystackMapsServiceErrorsToHTTPStatus(t *testing.T) {
	api := newTestAPIWithPaystack(testPaystackSecret)
	api.payments.webhookErr = domain.ErrNotFound

	body := paystackChargeBody("txn-abc-123", "cust-01", "NGN", 500000)
	sig := paystackSign([]byte(body))

	recorder := api.post(t, "/v1/webhooks/paystack", body, map[string]string{
		"X-Paystack-Signature": sig,
	})

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}
