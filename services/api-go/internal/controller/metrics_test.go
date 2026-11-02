package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-go/internal/domain"
)

type backlogStub struct {
	backlog domain.ReconciliationBacklog
	err     error
}

func (s backlogStub) Backlog(context.Context) (domain.ReconciliationBacklog, error) {
	return s.backlog, s.err
}

func TestMetricsExposeTheOperatorBacklog(t *testing.T) {
	handler := MetricsHandler(backlogStub{backlog: domain.ReconciliationBacklog{OpenCases: 3, OldestOpenCase: time.Hour, UnvendedCredits: 2, OldestUnvendedSTS: time.Minute}}, slog.New(slog.DiscardHandler))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		"ambagrid_api_reconciliation_open_cases 3",
		"ambagrid_api_reconciliation_oldest_open_case_age_seconds 3600",
		"ambagrid_api_sts_unvended_credits 2",
		"ambagrid_api_sts_oldest_unvended_credit_age_seconds 60",
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("metrics missing %q:\n%s", want, response.Body.String())
		}
	}
}

func TestMetricsFailTheScrapeWhenTheBacklogCannotBeRead(t *testing.T) {
	response := httptest.NewRecorder()
	MetricsHandler(backlogStub{err: errors.New("database down")}, slog.New(slog.DiscardHandler)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}
