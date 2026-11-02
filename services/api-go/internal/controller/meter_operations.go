package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"api-go/internal/domain"
)

type MeterOperationsService interface {
	Reconcile(context.Context, domain.ReconcileCommand) (domain.ReconciliationCase, error)
	Create(context.Context, domain.CreateMeterCommand) (domain.MeterCommand, error)
	Get(context.Context, string) (domain.MeterCommandDetail, error)
	Resolved(context.Context, int, string) ([]domain.ReconciliationCase, error)
	RefundShortfalls(context.Context, int, string) ([]domain.RefundShortfall, error)
}

type reconcileCommandBody struct {
	ReviewedConflictID    string `json:"reviewed_conflict_id,omitempty"`
	ReviewedConflictCount int64  `json:"reviewed_conflict_count,omitempty"`
	Owner                 string `json:"owner"`
	Finding               string `json:"finding"`
	Disposition           string `json:"disposition"`
	Reason                string `json:"reason"`
	Evidence              string `json:"evidence"`
	RefundReference       string `json:"refund_reference,omitempty"`
}

type createMeterCommandBody struct {
	RequestID      string `json:"request_id"`
	MeterID        string `json:"meter_id"`
	CommandType    string `json:"command_type"`
	Reason         string `json:"reason"`
	ConfirmMeterID string `json:"confirm_meter_id,omitempty"`
}

type reconciliationCaseDTO struct {
	CommandID            string    `json:"command_id"`
	State                string    `json:"state"`
	NeedsReview          bool      `json:"needs_review"`
	Owner                string    `json:"owner"`
	Finding              string    `json:"finding"`
	Disposition          string    `json:"disposition"`
	Reason               string    `json:"reason"`
	Evidence             string    `json:"evidence"`
	ResolvedBy           *string   `json:"resolved_by,omitempty"`
	ReplacementCommandID *string   `json:"replacement_command_id,omitempty"`
	RefundReference      *string   `json:"refund_reference,omitempty"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func toReconciliationCaseDTO(c domain.ReconciliationCase) reconciliationCaseDTO {
	return reconciliationCaseDTO{
		CommandID: c.CommandID, State: string(c.State), NeedsReview: c.NeedsReview, Owner: c.Owner,
		Finding: string(c.Finding), Disposition: string(c.Disposition), Reason: c.Reason, Evidence: c.Evidence,
		ResolvedBy: c.ResolvedBy, ReplacementCommandID: c.ReplacementCommandID, RefundReference: c.RefundReference, UpdatedAt: c.UpdatedAt,
	}
}

func toReconciliationCaseDTOPtr(c *domain.ReconciliationCase) *reconciliationCaseDTO {
	if c == nil {
		return nil
	}
	dto := toReconciliationCaseDTO(*c)
	return &dto
}

type commandObservationDTO struct {
	RelayClosed     *bool    `json:"relay_closed,omitempty"`
	BalanceKWh      *float64 `json:"balance_kwh,omitempty"`
	TamperDetected  *bool    `json:"tamper_detected,omitempty"`
	FirmwareVersion string   `json:"firmware_version,omitempty"`
	FaultCode       string   `json:"fault_code,omitempty"`
}

func (o *commandObservationDTO) toDomain() *domain.CommandObservation {
	if o == nil {
		return nil
	}
	return &domain.CommandObservation{RelayClosed: o.RelayClosed, BalanceKWh: o.BalanceKWh, TamperDetected: o.TamperDetected, FirmwareVersion: o.FirmwareVersion, FaultCode: o.FaultCode}
}

func toCommandObservationDTO(o *domain.CommandObservation) *commandObservationDTO {
	if o == nil {
		return nil
	}
	return &commandObservationDTO{RelayClosed: o.RelayClosed, BalanceKWh: o.BalanceKWh, TamperDetected: o.TamperDetected, FirmwareVersion: o.FirmwareVersion, FaultCode: o.FaultCode}
}

type retainedCommandReportDTO struct {
	ReportID      string                 `json:"report_id"`
	ConflictCount int64                  `json:"conflict_count"`
	Status        string                 `json:"status"`
	OccurredAt    time.Time              `json:"occurred_at"`
	ReceivedAt    time.Time              `json:"received_at"`
	FailureReason string                 `json:"failure_reason,omitempty"`
	Observation   *commandObservationDTO `json:"observation,omitempty"`
}

func toRetainedCommandReportDTO(r *domain.RetainedCommandReport) *retainedCommandReportDTO {
	if r == nil {
		return nil
	}
	return &retainedCommandReportDTO{ReportID: r.ReportID, ConflictCount: r.ConflictCount, Status: string(r.Status), OccurredAt: r.OccurredAt, ReceivedAt: r.ReceivedAt, FailureReason: r.FailureReason, Observation: toCommandObservationDTO(r.Observation)}
}

type refundShortfallDTO struct {
	CreditID            string    `json:"credit_id"`
	CommandID           string    `json:"command_id"`
	Reference           string    `json:"refund_reference"`
	ShortfallKWh        string    `json:"shortfall_kwh"`
	ShortfallMoneyMinor int64     `json:"shortfall_money_minor"`
	RefundedAt          time.Time `json:"refunded_at"`
}

func (a API) authenticateOperator(w http.ResponseWriter, r *http.Request) (string, bool) {
	actor, ok := a.OperatorAuth.ownerOf(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeErrorBody(w, a.logger(), http.StatusUnauthorized, codeUnauthenticated, "operator API key required", RequestIDFrom(r.Context()))
	}
	return actor, ok
}
func readOperation(w http.ResponseWriter, r *http.Request, dst any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return errInvalidParameter
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errInvalidParameter
	}
	return nil
}
func (a API) handleReconcileCommand(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.authenticateOperator(w, r)
	if !ok {
		return
	}
	var body reconcileCommandBody
	if err := readOperation(w, r, &body); err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	result, err := a.MeterOperations.Reconcile(r.Context(), domain.ReconcileCommand{
		ReviewedConflictID: body.ReviewedConflictID, ReviewedConflictCount: body.ReviewedConflictCount,
		CommandID: r.PathValue("command_id"), Actor: actor, Owner: body.Owner, Finding: domain.CaseFinding(body.Finding),
		Disposition: domain.CaseDisposition(body.Disposition), Reason: body.Reason, Evidence: body.Evidence, RefundReference: body.RefundReference,
	})
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	writeObject(w, r, a.logger(), toReconciliationCaseDTO(result))
}
func (a API) handleCreateMeterCommand(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.authenticateOperator(w, r)
	if !ok {
		return
	}
	var body createMeterCommandBody
	if err := readOperation(w, r, &body); err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	result, err := a.MeterOperations.Create(r.Context(), domain.CreateMeterCommand{
		RequestID: body.RequestID, MeterID: body.MeterID, Type: domain.MeterCommandType(body.CommandType),
		Reason: body.Reason, ConfirmMeterID: body.ConfirmMeterID, Actor: actor,
	})
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	writeObject(w, r, a.logger(), toMeterCommandDTO(result))
}
func (a API) handleGetMeterCommand(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authenticateOperator(w, r); !ok {
		return
	}
	result, err := a.MeterOperations.Get(r.Context(), r.PathValue("command_id"))
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	writeObject(w, r, a.logger(), struct {
		Command        meterCommandDTO           `json:"command"`
		LatestConflict *retainedCommandReportDTO `json:"latest_conflict,omitempty"`
		Observation    *commandObservationDTO    `json:"observation,omitempty"`
		ObservedAt     *time.Time                `json:"observed_at"`
		ReceivedAt     *time.Time                `json:"received_at"`
		Case           *reconciliationCaseDTO    `json:"case,omitempty"`
	}{toMeterCommandDTO(result.Command), toRetainedCommandReportDTO(result.LatestConflict), toCommandObservationDTO(result.Observation), result.ObservedAt, result.ReceivedAt, toReconciliationCaseDTOPtr(result.Case)})
}
func (a API) handleResolvedCases(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authenticateOperator(w, r); !ok {
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	result, err := a.MeterOperations.Resolved(r.Context(), limit, r.URL.Query().Get("after_command_id"))
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	cases := make([]reconciliationCaseDTO, len(result))
	for i, c := range result {
		cases[i] = toReconciliationCaseDTO(c)
	}
	writeObject(w, r, a.logger(), cases)
}

func (a API) handleRefundShortfalls(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authenticateOperator(w, r); !ok {
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	result, err := a.MeterOperations.RefundShortfalls(r.Context(), limit, r.URL.Query().Get("after_credit_id"))
	if err != nil {
		writeError(w, r, a.logger(), err)
		return
	}
	shortfalls := make([]refundShortfallDTO, len(result))
	for i, item := range result {
		shortfalls[i] = refundShortfallDTO{item.CreditID, item.CommandID, item.Reference, string(item.Shortfall.KWh), item.Shortfall.MoneyMinor, item.RefundedAt}
	}
	writeObject(w, r, a.logger(), shortfalls)
}

func (a API) handleMeterCommandTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authenticateOperator(w, r); !ok {
		return
	}
	type template struct {
		Type         domain.MeterCommandType `json:"command_type"`
		Label        string                  `json:"label"`
		Reason       string                  `json:"default_reason"`
		Confirmation bool                    `json:"requires_meter_confirmation"`
	}
	templates := make([]template, len(domain.OperatorCommandTemplates))
	for i, t := range domain.OperatorCommandTemplates {
		templates[i] = template{t.Type, t.Label, t.DefaultReason, t.NeedsConfirm}
	}
	writeObject(w, r, a.logger(), templates)
}
