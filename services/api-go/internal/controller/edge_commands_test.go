package controller

import (
	"context"
	"encoding/json"
	"fmt"
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

const testCommandID = "3f2b8c1e-5d4a-4e6b-9c7d-1a2b3c4d5e6f"

type fakeEdgeCommands struct {
	commands  []domain.EdgeCommand
	reported  domain.MeterCommand
	changed   bool
	err       error
	gotSite   string
	gotLimit  int
	gotReport domain.CommandReport
}

func (f *fakeEdgeCommands) Poll(_ context.Context, siteID string, limit int) ([]domain.EdgeCommand, error) {
	f.gotSite, f.gotLimit = siteID, limit
	return f.commands, f.err
}

func (f *fakeEdgeCommands) Report(_ context.Context, siteID string, report domain.CommandReport) (domain.MeterCommand, bool, error) {
	f.gotSite, f.gotReport = siteID, report
	return f.reported, f.changed, f.err
}

func newEdgeTestAPI(edge *fakeEdgeCommands) http.Handler {
	api := API{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		RequestTimeout: time.Second,
		EdgeAuth:       NewSiteKeys(map[string]string{"key-kaji": "ng-kaji-01", "key-epe": "lagos-epe"}),
	}
	if edge != nil {
		api.EdgeCommands = edge
	}
	return api.Handler()
}

func edgeRequest(method, path, key, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	return r
}

func TestEdgeCommandsRequireASiteKey(t *testing.T) {
	handler := newEdgeTestAPI(&fakeEdgeCommands{})
	for name, key := range map[string]string{"missing": "", "unknown": "key-other", "prefix": "key-kaj"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, edgeRequest(http.MethodPost, "/v1/edge/commands/poll", key, ""))
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s key: status %d, WWW-Authenticate %q; want 401 Bearer", name, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
	}
}

type fakeReconciliation struct {
	result    page.Page[domain.CommandReconciliation]
	gotSite   *string
	gotLimit  int
	gotCursor string
}

func (f *fakeReconciliation) ListReconciliation(_ context.Context, siteID *string, limit int, cursor string) (page.Page[domain.CommandReconciliation], error) {
	f.gotSite, f.gotLimit, f.gotCursor = siteID, limit, cursor
	return f.result, nil
}

func TestReconciliationIsAnOperatorListingAcrossSites(t *testing.T) {
	expiredAt := time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)
	reconciliation := &fakeReconciliation{result: page.Page[domain.CommandReconciliation]{
		Limit: 10, NextCursor: "next", Items: []domain.CommandReconciliation{
			{
				SiteCommand:   domain.SiteCommand{SiteID: "ng-kaji-01", Command: domain.MeterCommand{CommandID: testCommandID, Status: domain.CommandStatusExpired}},
				Site:          domain.Site{HealthStatus: domain.SiteHealthOffline},
				ConflictCount: 1, LatestConflict: &domain.CommandReport{Status: domain.CommandStatusSent},
			},
			{
				SiteCommand:     domain.SiteCommand{SiteID: "lagos-epe", Command: domain.MeterCommand{CommandID: testCommandID, Status: domain.CommandStatusAcknowledged}},
				LegacyExpiredAt: &expiredAt,
			},
		},
	}}
	handler := API{
		Logger:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		RequestTimeout:        time.Second,
		EdgeAuth:              NewSiteKeys(map[string]string{"key-kaji": "ng-kaji-01"}),
		EdgeCommands:          &fakeEdgeCommands{},
		CommandReconciliation: reconciliation,
	}.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/meter-commands/reconciliation", nil))
	if rec.Code != http.StatusOK || reconciliation.gotSite != nil {
		t.Fatalf("fleet-wide: status=%d site=%v: %s", rec.Code, reconciliation.gotSite, rec.Body.String())
	}
	var body collectionBody[commandReconciliationDTO]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 2 || body.Data[0].Reason != "conflicting_report" || body.Data[0].Status != "expired" || body.Data[0].SiteHealth != "offline" ||
		body.Data[1].Reason != "overturned_expiry" || body.Data[1].LegacyExpiredAt == nil || !body.Data[1].LegacyExpiredAt.Equal(expiredAt) || body.Page.NextCursor != "next" {
		t.Fatalf("body = %+v", body)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/meter-commands/reconciliation?site_id=lagos-epe&limit=10&cursor=before", nil))
	if rec.Code != http.StatusOK || reconciliation.gotSite == nil || *reconciliation.gotSite != "lagos-epe" || reconciliation.gotLimit != 10 || reconciliation.gotCursor != "before" {
		t.Fatalf("filtered: status=%d site=%v limit=%d cursor=%s", rec.Code, reconciliation.gotSite, reconciliation.gotLimit, reconciliation.gotCursor)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, edgeRequest(http.MethodGet, "/v1/edge/commands/reconciliation", "key-kaji", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("edge-key reconciliation route status = %d, want it removed", rec.Code)
	}
}

func TestConflictingReportIsAcceptedOnlyAfterServiceRetainsIt(t *testing.T) {
	edge := &fakeEdgeCommands{
		reported: domain.MeterCommand{CommandID: testCommandID, Status: domain.CommandStatusExpired},
		err:      services.ErrReportNeedsReconciliation,
	}
	rec := httptest.NewRecorder()
	newEdgeTestAPI(edge).ServeHTTP(rec, edgeRequest(http.MethodPost, "/v1/edge/commands/"+testCommandID+"/report", "key-kaji", `{"status":"sent","occurred_at":"2026-10-01T14:30:00Z"}`))
	var body objectBody[edgeCommandReportDTO]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusAccepted || !body.Data.ReconciliationRequired || body.Data.Changed || body.Data.Status != "expired" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPollEdgeCommandsReturnsTheAuthenticatedSitesCommands(t *testing.T) {
	requestedAt := time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)
	edge := &fakeEdgeCommands{commands: []domain.EdgeCommand{{
		CommandID: testCommandID, MeterID: "met-0101", DeviceID: "meter-01",
		CommandType: domain.CommandReconnectMeter, Status: domain.CommandStatusQueued,
		RequestedAt: requestedAt, ExpiresAt: requestedAt.Add(24 * time.Hour), ExpiresIn: 90 * time.Minute,
	}}}
	rec := httptest.NewRecorder()
	newEdgeTestAPI(edge).ServeHTTP(rec, edgeRequest(http.MethodPost, "/v1/edge/commands/poll?limit=10", "key-kaji", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if edge.gotSite != "ng-kaji-01" || edge.gotLimit != 10 {
		t.Fatalf("poll got site %q limit %d", edge.gotSite, edge.gotLimit)
	}
	var body struct {
		Data []edgeCommandDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].DeviceID != "meter-01" || body.Data[0].Status != "queued" ||
		!body.Data[0].ExpiresAt.Equal(requestedAt.Add(24*time.Hour)) || body.Data[0].ExpiresInMS != (90*time.Minute).Milliseconds() {
		t.Fatalf("poll body = %+v", body.Data)
	}

	edge.commands = nil
	rec = httptest.NewRecorder()
	newEdgeTestAPI(edge).ServeHTTP(rec, edgeRequest(http.MethodPost, "/v1/edge/commands/poll", "key-epe", ""))
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("empty poll body = %s, want an empty array", rec.Body)
	}
}

func TestReportEdgeCommandPassesTheReportThrough(t *testing.T) {
	edge := &fakeEdgeCommands{reported: domain.MeterCommand{CommandID: testCommandID, Status: domain.CommandStatusFailed}, changed: true}
	rec := httptest.NewRecorder()
	body := `{"status":"failed","occurred_at":"2026-10-01T14:31:00Z","failure_reason":"superseded by a newer relay command"}`
	newEdgeTestAPI(edge).ServeHTTP(rec, edgeRequest(http.MethodPost, "/v1/edge/commands/"+testCommandID+"/report", "key-kaji", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got := edge.gotReport
	if edge.gotSite != "ng-kaji-01" || got.CommandID != testCommandID || got.Status != domain.CommandStatusFailed ||
		!got.OccurredAt.Equal(time.Date(2026, 10, 1, 14, 31, 0, 0, time.UTC)) || got.FailureReason == "" {
		t.Fatalf("report = %+v for site %q", got, edge.gotSite)
	}
	if !strings.Contains(rec.Body.String(), `"changed":true`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestReportEdgeCommandMapsErrors(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		err  error
		want int
	}{
		"unknown field":  {body: `{"status":"sent","occurred_at":"2026-10-01T14:31:00Z","extra":1}`, want: http.StatusBadRequest},
		"invalid report": {err: fmt.Errorf("%w: occurred_at is required", services.ErrInvalidRequest), want: http.StatusBadRequest},
		"other site":     {err: fmt.Errorf("%w: meter command", domain.ErrNotFound), want: http.StatusNotFound},
		"contradiction":  {err: fmt.Errorf("%w: acknowledged cannot become failed", domain.ErrConflict), want: http.StatusConflict},
	} {
		body := c.body
		if body == "" {
			body = `{"status":"sent","occurred_at":"2026-10-01T14:31:00Z"}`
		}
		rec := httptest.NewRecorder()
		newEdgeTestAPI(&fakeEdgeCommands{err: c.err}).ServeHTTP(rec, edgeRequest(http.MethodPost, "/v1/edge/commands/"+testCommandID+"/report", "key-kaji", body))
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d; body %s", name, rec.Code, c.want, rec.Body)
		}
	}
}

func TestEdgeCommandRoutesAreAbsentWhenDisabled(t *testing.T) {
	rec := httptest.NewRecorder()
	newEdgeTestAPI(nil).ServeHTTP(rec, edgeRequest(http.MethodPost, "/v1/edge/commands/poll", "key-kaji", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when edge delivery is not configured", rec.Code)
	}
}
