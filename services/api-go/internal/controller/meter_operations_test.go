package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"api-go/internal/domain"
	"api-go/internal/services"
)

type operationRepo struct {
	calls   int
	created domain.CreateMeterCommand
}

func (f *operationRepo) RunMeterOperationsTx(_ context.Context, fn func(services.MeterOperationsTx) error) error {
	return fn(operationTx{repo: f})
}

// operationTx implements only what command creation calls; anything else panics.
type operationTx struct {
	services.MeterOperationsTx
	repo *operationRepo
}

func (operationTx) LockCommandRequest(context.Context, string) error { return nil }
func (operationTx) FindCommand(context.Context, string) (*domain.MeterCommand, error) {
	return nil, nil
}
func (operationTx) CommandProfile(context.Context, string) (domain.MeterCommandProfile, error) {
	return domain.MeterCommandProfile{SiteID: "site-1", RelayControl: true, StatusRead: true}, nil
}
func (t operationTx) InsertOperatorCommand(_ context.Context, r domain.CreateMeterCommand, _ domain.MeterCommandProfile) (domain.MeterCommand, error) {
	t.repo.calls++
	t.repo.created = r
	return domain.MeterCommand{CommandID: r.RequestID, MeterID: r.MeterID, CommandType: r.Type, RequestedBy: r.Actor}, nil
}
func (operationTx) InsertAuditEvent(context.Context, string, string, string, string, string, map[string]any) error {
	return nil
}
func (*operationRepo) ListRefundShortfalls(context.Context, int, string) ([]domain.RefundShortfall, error) {
	return nil, nil
}
func (*operationRepo) GetMeterCommand(context.Context, string) (domain.MeterCommandDetail, error) {
	return domain.MeterCommandDetail{}, nil
}
func (*operationRepo) ListResolvedCases(context.Context, int, string) ([]domain.ReconciliationCase, error) {
	return nil, nil
}
func TestMeterOperationsAuthenticateActorAndRequireRelayConfirmation(t *testing.T) {
	repo := &operationRepo{}
	handler := (API{MeterOperations: services.NewMeterOperationsService(repo), OperatorAuth: NewSiteKeys(map[string]string{"operator-key": "operator-alice"}), EdgeAuth: NewSiteKeys(map[string]string{"edge-key": "site-1"})}).Handler()
	body := `{"request_id":"` + testCommandID + `","meter_id":"meter-1","command_type":"disconnect_meter","reason":"Authorized work","confirm_meter_id":"meter-1"}`
	for _, key := range []string{"", "edge-key", "unknown"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, edgeRequest(http.MethodPost, "/v1/meter-commands", key, body))
		if response.Code != http.StatusUnauthorized || repo.calls != 0 {
			t.Fatalf("unauthorized mutation: %d calls %d", response.Code, repo.calls)
		}
	}
	missingConfirmation := `{"request_id":"` + testCommandID + `","meter_id":"meter-1","command_type":"disconnect_meter","reason":"Authorized work"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, edgeRequest(http.MethodPost, "/v1/meter-commands", "operator-key", missingConfirmation))
	if response.Code != http.StatusBadRequest || repo.calls != 0 {
		t.Fatalf("missing relay confirmation accepted: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, edgeRequest(http.MethodPost, "/v1/meter-commands", "operator-key", body))
	if response.Code != http.StatusOK || repo.calls != 1 || repo.created.Actor != "operator-alice" {
		t.Fatalf("command authentication: %d %+v", response.Code, repo.created)
	}
	var result objectBody[meterCommandDTO]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.CommandID != testCommandID {
		t.Fatal("wrong command response")
	}
}
func TestOperatorTemplatesExcludeUnfundedCreditCommands(t *testing.T) {
	handler := (API{MeterOperations: services.NewMeterOperationsService(&operationRepo{}), OperatorAuth: NewSiteKeys(map[string]string{"operator-key": "operator-alice"})}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, edgeRequest(http.MethodGet, "/v1/meter-command-templates", "operator-key", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("templates: %d %s", response.Code, response.Body.String())
	}
	var result objectBody[[]struct {
		Type         string `json:"command_type"`
		Confirmation bool   `json:"requires_meter_confirmation"`
	}]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 3 {
		t.Fatalf("templates: %+v", result.Data)
	}
	for _, template := range result.Data {
		if template.Type == "credit_meter" || (template.Type != "request_meter_status" && !template.Confirmation) {
			t.Fatalf("unsafe template: %+v", template)
		}
	}
}

func TestProductionWiringRegistersEveryOperatorRoute(t *testing.T) {
	handler := (API{
		MeterOperations: services.NewMeterOperationsService(&operationRepo{}), CommandReconciliation: &fakeReconciliation{},
		OperatorAuth: NewSiteKeys(map[string]string{"operator-key": "operator-alice"}),
	}).Handler()
	for _, route := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/v1/meter-commands/reconciliation", http.StatusOK},
		{http.MethodGet, "/v1/meter-commands/reconciliation/resolved", http.StatusOK},
		{http.MethodGet, "/v1/meter-commands/refund-shortfalls", http.StatusOK},
		{http.MethodGet, "/v1/meter-commands/" + testCommandID, http.StatusOK},
		{http.MethodPost, "/v1/meter-commands/reconciliation", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/meter-commands/refund-shortfalls", http.StatusMethodNotAllowed},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, edgeRequest(route.method, route.path, "operator-key", ""))
		if response.Code != route.want {
			t.Errorf("%s %s = %d, want %d: %s", route.method, route.path, response.Code, route.want, response.Body.String())
		}
	}
}
