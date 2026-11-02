package commands_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"edge-agent-go/internal/commands"
	"edge-agent-go/internal/domain"
)

func TestHTTPReportCarriesObservation(t *testing.T) {
	var received struct {
		Observation *domain.CommandObservation `json:"observation"`
		Status      string                     `json:"status"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing site authentication")
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	relay, tamper, balance := false, true, 7.5
	report := domain.CommandReport{CommandID: "status-1", Status: domain.ReportAcknowledged, OccurredAt: time.Now(), Observation: &domain.CommandObservation{RelayClosed: &relay, TamperDetected: &tamper, BalanceKWh: &balance, FirmwareVersion: "1.2.3", FaultCode: "E_TEST"}}
	if err := commands.NewHTTPClient(server.URL, "test-key", time.Second).Report(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if received.Observation == nil || received.Status != "acknowledged" || received.Observation.TamperDetected == nil || !*received.Observation.TamperDetected || received.Observation.RelayClosed == nil || *received.Observation.RelayClosed || received.Observation.FirmwareVersion != "1.2.3" {
		t.Fatalf("lost wire evidence: %+v", received)
	}
}
