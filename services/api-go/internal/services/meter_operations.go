package services

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"api-go/internal/domain"
)

type MeterOperationsRepository interface {
	RunMeterOperationsTx(context.Context, func(MeterOperationsTx) error) error
	GetMeterCommand(context.Context, string) (domain.MeterCommandDetail, error)
	ListResolvedCases(context.Context, int, string) ([]domain.ReconciliationCase, error)
	ListRefundShortfalls(context.Context, int, string) ([]domain.RefundShortfall, error)
}

type MeterOperationsTx interface {
	LockReconcileTarget(ctx context.Context, commandID string) (domain.ReconcileTarget, error)
	LoadCase(ctx context.Context, commandID string) (*domain.ReconciliationCase, error)
	LatestConflict(ctx context.Context, commandID string) (*domain.RetainedCommandReport, error)
	ClearCaseReview(ctx context.Context, commandID string) error
	InvestigatedByAnother(ctx context.Context, commandID, actor string) (bool, error)
	HasRetainedEvidence(ctx context.Context, commandID string) (bool, error)
	CreditSettlement(ctx context.Context, commandID, creditID string) (domain.CreditSettlement, error)
	RedeliveryAllowed(ctx context.Context, meterID, creditID string) (bool, error)
	InsertRedelivery(ctx context.Context, target domain.ReconcileTarget, actor, reason string) (string, error)
	RefundableCredit(ctx context.Context, creditID string) (*domain.RefundableCredit, error)
	RecordRefund(ctx context.Context, target domain.ReconcileTarget, r domain.ReconcileCommand, plan domain.CreditReversalPlan) error
	SaveCase(ctx context.Context, c domain.ReconciliationCase) (domain.ReconciliationCase, error)
	InsertAuditEvent(ctx context.Context, siteID, actorID, action, subjectType, subjectID string, metadata map[string]any) error
	LockCommandRequest(ctx context.Context, requestID string) error
	FindCommand(ctx context.Context, commandID string) (*domain.MeterCommand, error)
	CommandProfile(ctx context.Context, meterID string) (domain.MeterCommandProfile, error)
	InsertOperatorCommand(ctx context.Context, r domain.CreateMeterCommand, profile domain.MeterCommandProfile) (domain.MeterCommand, error)
}

type MeterOperationsService struct {
	repository MeterOperationsRepository
	now        func() time.Time
}

func NewMeterOperationsService(r MeterOperationsRepository) *MeterOperationsService {
	return &MeterOperationsService{repository: r, now: time.Now}
}

func conflict(message string) error { return fmt.Errorf("%w: %s", domain.ErrConflict, message) }

func (s *MeterOperationsService) Reconcile(ctx context.Context, r domain.ReconcileCommand) (domain.ReconciliationCase, error) {
	if err := domain.ValidateCommandID(r.CommandID); err != nil {
		return domain.ReconciliationCase{}, err
	}
	r.CommandID = strings.ToLower(r.CommandID)
	r.ReviewedConflictID = strings.ToLower(r.ReviewedConflictID)
	r.Actor, r.Owner = strings.TrimSpace(r.Actor), strings.TrimSpace(r.Owner)
	r.Reason, r.Evidence = strings.TrimSpace(r.Reason), strings.TrimSpace(r.Evidence)
	r.RefundReference = strings.TrimSpace(r.RefundReference)
	invalid := func(message string) (domain.ReconciliationCase, error) {
		return domain.ReconciliationCase{}, fmt.Errorf("%w: %s", ErrInvalidRequest, message)
	}
	if r.Actor == "" || r.Reason == "" || r.Evidence == "" {
		return invalid("actor, reason and evidence are required")
	}
	if len(r.Reason) > 1000 || len(r.Evidence) > 8000 || len(r.Owner) > 200 || len(r.RefundReference) > 200 {
		return invalid("resolution fields exceed maximum length")
	}
	switch r.Disposition {
	case domain.DispositionReview:
		if r.ReviewedConflictCount < 1 {
			return invalid("review requires the displayed conflict count")
		}
		if err := domain.ValidateCommandID(r.ReviewedConflictID); err != nil {
			return invalid("review requires the conflict report ID shown to the operator")
		}
		if r.Finding != domain.FindingUnknown {
			return invalid("review records an assessment without changing the original finding")
		}
	case domain.DispositionInvestigate:
		if r.Finding != domain.FindingUnknown || r.Owner == "" {
			return invalid("investigation requires an owner and unknown finding")
		}
	case domain.DispositionDelivered:
		if r.Finding != domain.FindingDelivered {
			return invalid("delivered resolution requires confirmed delivery evidence")
		}
	case domain.DispositionRedeliver, domain.DispositionRefund:
		if r.Finding != domain.FindingNotDelivered {
			return invalid("compensation requires confirmed non-delivery evidence")
		}
		if r.Disposition == domain.DispositionRefund && r.RefundReference == "" {
			return invalid("refund requires an external refund reference")
		}
	default:
		return invalid("unsupported disposition")
	}
	if r.Disposition != domain.DispositionRefund && r.RefundReference != "" {
		return invalid("refund_reference is only allowed for refunds")
	}
	if r.Owner == "" {
		r.Owner = r.Actor
	}
	var result domain.ReconciliationCase
	err := s.repository.RunMeterOperationsTx(ctx, func(tx MeterOperationsTx) (err error) {
		result, err = s.reconcile(ctx, tx, r)
		return err
	})
	if err != nil {
		return domain.ReconciliationCase{}, err
	}
	return result, nil
}

func (s *MeterOperationsService) reconcile(ctx context.Context, tx MeterOperationsTx, r domain.ReconcileCommand) (domain.ReconciliationCase, error) {
	target, err := tx.LockReconcileTarget(ctx, r.CommandID)
	if err != nil {
		return domain.ReconciliationCase{}, err
	}
	previous, err := tx.LoadCase(ctx, r.CommandID)
	if err != nil {
		return domain.ReconciliationCase{}, err
	}
	if previous != nil && previous.State == domain.CaseResolved {
		return s.revisitResolvedCase(ctx, tx, target, *previous, r)
	}
	if r.Disposition == domain.DispositionReview {
		return domain.ReconciliationCase{}, conflict("only a resolved case can be reviewed")
	}
	if err := s.requireReconciliationWork(ctx, tx, target, previous); err != nil {
		return domain.ReconciliationCase{}, err
	}
	saved := domain.ReconciliationCase{
		CommandID: r.CommandID, State: domain.CaseResolved, Owner: r.Owner, Finding: r.Finding,
		Disposition: r.Disposition, Reason: r.Reason, Evidence: r.Evidence, ResolvedBy: &r.Actor,
	}
	auditMetadata := map[string]any{}
	switch r.Disposition {
	case domain.DispositionInvestigate:
		saved.State, saved.ResolvedBy = domain.CaseInvestigating, nil
	case domain.DispositionDelivered:
		if err := requireDeliveryEvidence(ctx, tx, target, previous, r.Actor); err != nil {
			return domain.ReconciliationCase{}, err
		}
	case domain.DispositionRedeliver:
		if err := requireCompensation(ctx, tx, target); err != nil {
			return domain.ReconciliationCase{}, err
		}
		allowed, err := tx.RedeliveryAllowed(ctx, target.Command.MeterID, *target.CreditID)
		if err != nil {
			return domain.ReconciliationCase{}, err
		}
		if !allowed {
			return domain.ReconciliationCase{}, conflict("meter is not commissioned for redelivery or its assignment changed")
		}
		replacement, err := tx.InsertRedelivery(ctx, target, r.Actor, r.Reason)
		if err != nil {
			return domain.ReconciliationCase{}, err
		}
		saved.ReplacementCommandID = &replacement
	case domain.DispositionRefund:
		if err := requireCompensation(ctx, tx, target); err != nil {
			return domain.ReconciliationCase{}, err
		}
		plan, err := planRefund(ctx, tx, *target.CreditID)
		if err != nil {
			return domain.ReconciliationCase{}, err
		}
		if err := tx.RecordRefund(ctx, target, r, plan); err != nil {
			return domain.ReconciliationCase{}, err
		}
		auditMetadata["refund_shortfall"] = plan.Shortfall
		saved.RefundReference = &r.RefundReference
	}
	result, err := tx.SaveCase(ctx, saved)
	if err != nil {
		return domain.ReconciliationCase{}, err
	}
	auditMetadata["case"] = result
	if err := tx.InsertAuditEvent(ctx, target.SiteID, r.Actor, "meter_command.reconciliation."+string(r.Disposition), "meter_command", r.CommandID, auditMetadata); err != nil {
		return domain.ReconciliationCase{}, err
	}
	return result, nil
}

// revisitResolvedCase keeps the original decision: only a review or an exact
// replay of that decision succeeds.
func (s *MeterOperationsService) revisitResolvedCase(ctx context.Context, tx MeterOperationsTx, target domain.ReconcileTarget, previous domain.ReconciliationCase, r domain.ReconcileCommand) (domain.ReconciliationCase, error) {
	if r.Disposition == domain.DispositionReview {
		latest, err := tx.LatestConflict(ctx, r.CommandID)
		if err != nil {
			return domain.ReconciliationCase{}, err
		}
		if latest == nil || latest.ReportID != r.ReviewedConflictID || latest.ConflictCount != r.ReviewedConflictCount {
			return domain.ReconciliationCase{}, conflict("new evidence arrived; reload the case before reviewing")
		}
		if err := tx.ClearCaseReview(ctx, r.CommandID); err != nil {
			return domain.ReconciliationCase{}, err
		}
		if err := tx.InsertAuditEvent(ctx, target.SiteID, r.Actor, "meter_command.reconciliation.review", "meter_command", r.CommandID, map[string]any{"reason": r.Reason, "evidence": r.Evidence, "reviewed_conflict_id": r.ReviewedConflictID, "reviewed_conflict_count": r.ReviewedConflictCount}); err != nil {
			return domain.ReconciliationCase{}, err
		}
		previous.NeedsReview = false
		return previous, nil
	}
	if previous.Disposition != r.Disposition || previous.Finding != r.Finding || previous.Evidence != r.Evidence || previous.Reason != r.Reason || (previous.RefundReference != nil && *previous.RefundReference != r.RefundReference) {
		return domain.ReconciliationCase{}, conflict("case already resolved with a different decision")
	}
	return previous, nil
}

func (s *MeterOperationsService) requireReconciliationWork(ctx context.Context, tx MeterOperationsTx, target domain.ReconcileTarget, previous *domain.ReconciliationCase) error {
	cmd := target.Command
	retained, err := tx.HasRetainedEvidence(ctx, cmd.CommandID)
	if err != nil {
		return err
	}
	overdue := (cmd.Status == domain.CommandStatusQueued || cmd.Status == domain.CommandStatusSent) && !cmd.ExpiresAt.After(s.now())
	if !retained && previous == nil && !cmd.UndeliveredCredit() && !overdue {
		return conflict("command has no reconciliation work")
	}
	return nil
}

func requireSettledCredit(target domain.ReconcileTarget) error {
	cmd := target.Command
	switch cmd.Status {
	case domain.CommandStatusRequested, domain.CommandStatusQueued, domain.CommandStatusSent:
		return conflict("device outcome remains unsettled; investigate before resolution")
	}
	if !cmd.UndeliveredCredit() || target.CreditID == nil || cmd.CreditKWh == nil {
		return conflict("this decision requires a funded, failed or expired credit")
	}
	return nil
}

// requireCompensation never compensates a historical attempt, a refunded credit,
// or an entitlement with positive or uncertain delivery evidence.
func requireCompensation(ctx context.Context, tx MeterOperationsTx, target domain.ReconcileTarget) error {
	if err := requireSettledCredit(target); err != nil {
		return err
	}
	settlement, err := tx.CreditSettlement(ctx, target.Command.CommandID, *target.CreditID)
	if err != nil {
		return err
	}
	if settlement.Finalized() || settlement.UncertainDelivery {
		return conflict("credit already settled, superseded, or has uncertain/positive delivery evidence")
	}
	return nil
}

// requireDeliveryEvidence stops prose alone settling a credit: without a device
// acknowledgement, a second operator must have opened the investigation.
func requireDeliveryEvidence(ctx context.Context, tx MeterOperationsTx, target domain.ReconcileTarget, previous *domain.ReconciliationCase, actor string) error {
	if target.CreditID == nil {
		return nil
	}
	settlement, err := tx.CreditSettlement(ctx, target.Command.CommandID, *target.CreditID)
	if err != nil {
		return err
	}
	if settlement.Superseded || settlement.Refunded {
		return conflict("credit was already replaced or refunded")
	}
	if settlement.HasDeviceAcknowledgement() {
		return nil
	}
	if previous == nil || previous.State != domain.CaseInvestigating {
		return conflict("delivery without a device acknowledgement requires an open investigation")
	}
	investigated, err := tx.InvestigatedByAnother(ctx, target.Command.CommandID, actor)
	if err != nil {
		return err
	}
	if !investigated {
		return conflict("delivery without a device acknowledgement requires an investigation opened by a different operator")
	}
	return nil
}

// planRefund reverses what the balance holds; the external refund already
// happened, so a consumed balance becomes a recorded shortfall rather than a refusal.
func planRefund(ctx context.Context, tx MeterOperationsTx, creditID string) (domain.CreditReversalPlan, error) {
	refundable, err := tx.RefundableCredit(ctx, creditID)
	if err != nil {
		return domain.CreditReversalPlan{}, err
	}
	if refundable == nil {
		return domain.CreditReversalPlan{}, conflict("only a paid meter credit can be refunded")
	}
	return domain.PlanCreditReversal(refundable.Credit, refundable.Balance)
}

func (s *MeterOperationsService) Create(ctx context.Context, r domain.CreateMeterCommand) (domain.MeterCommand, error) {
	if err := domain.ValidateCommandID(r.RequestID); err != nil {
		return domain.MeterCommand{}, err
	}
	r.RequestID = strings.ToLower(r.RequestID)
	r.MeterID, r.Reason = strings.TrimSpace(r.MeterID), strings.TrimSpace(r.Reason)
	if r.Actor == "" || r.MeterID == "" || r.Reason == "" || len(r.Reason) > 1000 {
		return domain.MeterCommand{}, fmt.Errorf("%w: meter, actor and reason are required", ErrInvalidRequest)
	}
	index := slices.IndexFunc(domain.OperatorCommandTemplates, func(t domain.OperatorCommandTemplate) bool { return t.Type == r.Type })
	if index < 0 {
		return domain.MeterCommand{}, fmt.Errorf("%w: only status and relay commands can be created manually", ErrInvalidRequest)
	}
	if domain.OperatorCommandTemplates[index].NeedsConfirm && r.ConfirmMeterID != r.MeterID {
		return domain.MeterCommand{}, fmt.Errorf("%w: confirm_meter_id must match the target meter", ErrInvalidRequest)
	}
	var created domain.MeterCommand
	err := s.repository.RunMeterOperationsTx(ctx, func(tx MeterOperationsTx) (err error) {
		created, err = createOperatorCommand(ctx, tx, r)
		return err
	})
	if err != nil {
		return domain.MeterCommand{}, err
	}
	return created, nil
}

// One request UUID names one immutable operation, including concurrent retries.
func createOperatorCommand(ctx context.Context, tx MeterOperationsTx, r domain.CreateMeterCommand) (domain.MeterCommand, error) {
	if err := tx.LockCommandRequest(ctx, r.RequestID); err != nil {
		return domain.MeterCommand{}, err
	}
	prior, err := tx.FindCommand(ctx, r.RequestID)
	if err != nil {
		return domain.MeterCommand{}, err
	}
	if prior != nil {
		if prior.MeterID != r.MeterID || prior.CommandType != r.Type || prior.RequestedBy != r.Actor || prior.Reason != r.Reason {
			return domain.MeterCommand{}, conflict("request_id already names a different command")
		}
		return *prior, nil
	}
	profile, err := tx.CommandProfile(ctx, r.MeterID)
	if err != nil {
		return domain.MeterCommand{}, err
	}
	if (r.Type == domain.CommandRequestMeterStatus && !profile.StatusRead) || (r.Type != domain.CommandRequestMeterStatus && !profile.RelayControl) {
		return domain.MeterCommand{}, conflict("meter capability not commissioned")
	}
	if r.Type == domain.CommandReconnectMeter && profile.BalanceExhausted {
		return domain.MeterCommand{}, conflict("customer balance is exhausted; a reconnect would supply unpaid energy")
	}
	cmd, err := tx.InsertOperatorCommand(ctx, r, profile)
	if err != nil {
		return domain.MeterCommand{}, err
	}
	metadata := map[string]any{"command_type": r.Type, "reason": r.Reason, "confirmed_meter_id": r.ConfirmMeterID}
	if err := tx.InsertAuditEvent(ctx, profile.SiteID, r.Actor, "meter_command.requested", "meter_command", cmd.CommandID, metadata); err != nil {
		return domain.MeterCommand{}, err
	}
	return cmd, nil
}
func (s *MeterOperationsService) Get(ctx context.Context, id string) (domain.MeterCommandDetail, error) {
	if err := domain.ValidateCommandID(id); err != nil {
		return domain.MeterCommandDetail{}, err
	}
	return s.repository.GetMeterCommand(ctx, id)
}
func (s *MeterOperationsService) Resolved(ctx context.Context, limit int, after string) ([]domain.ReconciliationCase, error) {
	limit, err := operationsPage(limit, after)
	if err != nil {
		return nil, err
	}
	return s.repository.ListResolvedCases(ctx, limit, after)
}

func (s *MeterOperationsService) RefundShortfalls(ctx context.Context, limit int, after string) ([]domain.RefundShortfall, error) {
	limit, err := operationsPage(limit, after)
	if err != nil {
		return nil, err
	}
	return s.repository.ListRefundShortfalls(ctx, limit, after)
}

func operationsPage(limit int, after string) (int, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return 0, fmt.Errorf("%w: limit must be between 1 and 200", ErrInvalidRequest)
	}
	if after != "" {
		if err := domain.ValidateCommandID(after); err != nil {
			return 0, err
		}
	}
	return limit, nil
}
