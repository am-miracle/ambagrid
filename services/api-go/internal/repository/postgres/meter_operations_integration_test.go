package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/services"
)

type operationsFixture struct {
	edgeFixture
	ops    *services.MeterOperationsService
	credit domain.ApplyPaymentResult
}

func newOperationsFixture(t *testing.T) operationsFixture {
	t.Helper()
	f := newEdgeFixture(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	customer, tariff := f.siteID+"-customer", f.siteID+"-tariff"
	exec(`UPDATE smart_meters SET credit_mode='meter' WHERE meter_id=$1`, f.meterID)
	exec(`INSERT INTO meter_command_profiles(meter_id,relay_control,status_read,prepaid_credit) VALUES($1,true,true,true)`, f.meterID)
	exec(`INSERT INTO customers(customer_id,site_id,display_name) VALUES($1,$2,'Operations test')`, customer, f.siteID)
	exec(`INSERT INTO meter_assignments(site_id,meter_id,customer_id) VALUES($1,$2,$3)`, f.siteID, f.meterID, customer)
	exec(`INSERT INTO tariff_plans(tariff_plan_id,site_id,name,currency,price_per_kwh,effective_from) VALUES($1,$2,'Operations test','NGN',250,now()-interval '1 day')`, tariff, f.siteID)
	store := NewStore(f.pool, 5*time.Second)
	credit, err := services.NewPaymentService(store).ApplyWebhookPayment(ctx, domain.ApplyPaymentCommand{Provider: "test", ExternalReference: f.siteID, CustomerID: customer, AmountMinorUnits: 25000, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE meter_commands SET status='expired' WHERE command_id=$1::uuid`, credit.MeterCommand.CommandID)
	clean := cleaner(t, f.pool)
	t.Cleanup(func() {
		clean(`DELETE FROM meter_command_observations WHERE command_id IN(SELECT command_id FROM meter_commands WHERE meter_id=$1)`, f.meterID)
		clean(`DELETE FROM command_reconciliation_cases WHERE command_id IN(SELECT command_id FROM meter_commands WHERE meter_id=$1)`, f.meterID)
		clean(`DELETE FROM meter_credit_refunds WHERE credit_id=$1::uuid`, credit.Credit.CreditID)
		clean(`DELETE FROM meter_commands WHERE meter_id=$1`, f.meterID)
		clean(`DELETE FROM command_event_outbox WHERE aggregate_id=ANY($1)`, []string{credit.Payment.PaymentID, credit.Credit.CreditID, credit.MeterCommand.CommandID})
		clean(`UPDATE credit_balances SET remaining_kwh=0,remaining_money_value_minor_units=0 WHERE assignment_id=$1::uuid`, credit.Credit.AssignmentID)
		clean(`DELETE FROM credit_balances WHERE assignment_id=$1::uuid`, credit.Credit.AssignmentID)
		clean(`DELETE FROM energy_credits WHERE credit_id=$1::uuid`, credit.Credit.CreditID)
		clean(`DELETE FROM payments WHERE payment_id=$1::uuid`, credit.Payment.PaymentID)
		clean(`DELETE FROM meter_assignments WHERE customer_id=$1`, customer)
		clean(`DELETE FROM tariff_plans WHERE tariff_plan_id=$1`, tariff)
		clean(`DELETE FROM customers WHERE customer_id=$1`, customer)
		clean(`DELETE FROM meter_command_profiles WHERE meter_id=$1`, f.meterID)
	})
	return operationsFixture{f, services.NewMeterOperationsService(store), credit}
}
func (f operationsFixture) decision(disposition domain.CaseDisposition) domain.ReconcileCommand {
	return domain.ReconcileCommand{CommandID: f.credit.MeterCommand.CommandID, Actor: "operator:test", Finding: "not_delivered", Disposition: disposition, Reason: "Device rejected credit", Evidence: "Device ledger inspected; no applied entry"}
}
func TestCreditRedeliveryIsConcurrentAndRepeatSafe(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	request := f.decision("redeliver")
	var wg sync.WaitGroup
	results := make([]domain.ReconciliationCase, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = f.ops.Reconcile(ctx, request) }(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].ReplacementCommandID == nil || *results[0].ReplacementCommandID != *results[1].ReplacementCommandID {
		t.Fatalf("duplicate replacement: %+v", results)
	}
	replacement := *results[0].ReplacementCommandID
	if f.status(t, request.CommandID) != domain.CommandStatusExpired || f.status(t, replacement) != domain.CommandStatusRequested {
		t.Fatal("history changed or replacement missing")
	}
	if events := f.outboxEvents(t, replacement); len(events) != 1 {
		t.Fatalf("replacement events: %v", events)
	}
	var n int
	var kwh float64
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM energy_credits WHERE credit_id=$1::uuid`, f.credit.Credit.CreditID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("funding duplicated: %d %v", n, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT remaining_kwh FROM credit_balances WHERE assignment_id=$1::uuid`, f.credit.Credit.AssignmentID).Scan(&kwh); err != nil || kwh != f.credit.Balance.RemainingKWh {
		t.Fatalf("balance changed: %f %v", kwh, err)
	}
	queue, err := f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(queue.Items) != 0 {
		t.Fatalf("resolved credit still open: %+v %v", queue, err)
	}
	refund := f.decision("refund")
	refund.RefundReference = "refund-" + f.siteID
	if _, err = f.ops.Reconcile(ctx, refund); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second compensation allowed: %v", err)
	}
	// A late acknowledgement is retained and makes the resolved case visible again.
	if _, _, err = f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: request.CommandID, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now()}); !errors.Is(err, services.ErrReportNeedsReconciliation) {
		t.Fatal(err)
	}
	queue, err = f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(queue.Items) != 1 || !queue.Items[0].NeedsReview || queue.Items[0].Case == nil {
		t.Fatalf("late evidence hidden: %+v %v", queue, err)
	}
	review := request
	review.Disposition = "review"
	review.Finding = "unknown"
	review.Reason = "Late acknowledgement reviewed"
	review.Evidence = "Escalated possible duplicate delivery to operations"
	detail, err := f.ops.Get(ctx, request.CommandID)
	if err != nil || detail.LatestConflict == nil {
		t.Fatalf("missing review evidence: %+v %v", detail, err)
	}
	review.ReviewedConflictID = detail.LatestConflict.ReportID
	review.ReviewedConflictCount = detail.LatestConflict.ConflictCount
	commands, err := f.service.Poll(ctx, f.siteID, 0)
	if err != nil || len(commands) != 0 || f.status(t, replacement) != domain.CommandStatusRequested {
		t.Fatalf("dispatched replacement after late acknowledgement: %+v %v", commands, err)
	}
	if _, err = f.ops.Reconcile(ctx, review); err != nil {
		t.Fatal(err)
	}
	queue, err = f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(queue.Items) != 0 {
		t.Fatalf("review did not close work: %+v %v", queue, err)
	}
	// A new device timestamp is new evidence, rather than a replay.
	if _, _, err = f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: request.CommandID, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now().Add(time.Minute)}); !errors.Is(err, services.ErrReportNeedsReconciliation) {
		t.Fatal(err)
	}
	if _, err = f.ops.Reconcile(ctx, review); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale evidence review accepted: %v", err)
	}
	// A genuinely new report does reopen it, even after review.
	queue, err = f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(queue.Items) != 1 {
		t.Fatalf("new evidence lost after review: %+v %v", queue, err)
	}
	// Clean replacement outbox before fixture removes the attempt chain.
	t.Cleanup(func() { cleaner(t, f.pool)(`DELETE FROM command_event_outbox WHERE aggregate_id=$1`, replacement) })
}
func TestRefundReversesEntitlementOnce(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	request := f.decision("refund")
	request.RefundReference = "external-refund-" + f.siteID
	for range 2 {
		if _, err := f.ops.Reconcile(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	var kwh float64
	var money int64
	if err := f.pool.QueryRow(ctx, `SELECT remaining_kwh,remaining_money_value_minor_units FROM credit_balances WHERE assignment_id=$1::uuid`, f.credit.Credit.AssignmentID).Scan(&kwh, &money); err != nil || kwh != 0 || money != 0 {
		t.Fatalf("refund accounting: %f %d %v", kwh, money, err)
	}
	if _, err := f.ops.Reconcile(ctx, f.decision("redeliver")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("redelivery after refund: %v", err)
	}
}
func TestCompensationRejectsUnknownDeliveryAndConsumedBalance(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	id := f.credit.MeterCommand.CommandID
	if _, err := f.pool.Exec(ctx, `UPDATE meter_commands SET status='sent',sent_at=now() WHERE command_id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ops.Reconcile(ctx, f.decision("redeliver")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("compensated unknown outcome: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE meter_commands SET status='expired' WHERE command_id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE credit_balances SET remaining_kwh=0,remaining_money_value_minor_units=0 WHERE assignment_id=$1::uuid`, f.credit.Credit.AssignmentID); err != nil {
		t.Fatal(err)
	}
	r := f.decision("refund")
	r.RefundReference = "refund-" + f.siteID
	if _, err := f.ops.Reconcile(ctx, r); err != nil {
		t.Fatalf("verified refund of a consumed entitlement refused: %v", err)
	}
	var reversed string
	var shortfallMoney int64
	if err := f.pool.QueryRow(ctx, `SELECT kwh::text,shortfall_money_minor FROM meter_credit_refunds WHERE credit_id=$1::uuid`, f.credit.Credit.CreditID).Scan(&reversed, &shortfallMoney); err != nil {
		t.Fatal(err)
	}
	if reversed != "0.000000" || shortfallMoney != *f.credit.Credit.MoneyValueMinorUnits {
		t.Fatalf("reversed %s kWh with %d shortfall", reversed, shortfallMoney)
	}
	shortfalls, err := f.ops.RefundShortfalls(ctx, 200, "")
	if err != nil || !slices.ContainsFunc(shortfalls, func(s domain.RefundShortfall) bool { return s.CreditID == f.credit.Credit.CreditID }) {
		t.Fatalf("shortfall not listed: %+v %v", shortfalls, err)
	}
	listed, err := f.service.ListReconciliation(ctx, &f.siteID, 10, "")
	if err != nil || len(listed.Items) != 0 {
		t.Fatalf("refunded credit still listed: %+v %v", listed.Items, err)
	}
}
func TestOperatorStatusCommandPersistsDeviceEvidence(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	var id string
	if err := f.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	request := domain.CreateMeterCommand{RequestID: id, MeterID: f.meterID, Type: domain.CommandRequestMeterStatus, Reason: "Investigate delivery", Actor: "operator:test"}
	for range 2 {
		if _, err := f.ops.Create(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	request.Type = domain.CommandDisconnectMeter
	request.ConfirmMeterID = f.meterID
	if _, err := f.ops.Create(ctx, request); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reused operation UUID accepted: %v", err)
	}
	relay, tamper, balance := true, true, 12.25
	if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now(), Observation: &domain.CommandObservation{RelayClosed: &relay, TamperDetected: &tamper, BalanceKWh: &balance, FirmwareVersion: "1.2.3"}}); err != nil {
		t.Fatal(err)
	}
	detail, err := f.ops.Get(ctx, id)
	if err != nil || detail.Observation == nil || !*detail.Observation.TamperDetected || detail.ObservedAt == nil || detail.ReceivedAt == nil {
		t.Fatalf("missing observation: %+v %v", detail, err)
	}
	// An identical snapshot is repeat-safe; changed device evidence is retained.
	observation := detail.Observation
	if _, changed, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now(), Observation: observation}); err != nil || changed {
		t.Fatalf("duplicate observation: changed=%t err=%v", changed, err)
	}
	changedObservation := *observation
	clear := false
	changedObservation.TamperDetected = &clear
	if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now(), Observation: &changedObservation}); !errors.Is(err, services.ErrReportNeedsReconciliation) {
		t.Fatalf("changed observation discarded: %v", err)
	}
	t.Cleanup(func() { cleaner(t, f.pool)(`DELETE FROM command_event_outbox WHERE aggregate_id=$1`, id) })
}

func TestReconciliationRecordsOwnershipWithoutSettlingUnknownDelivery(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	request := f.decision("investigate")
	request.Finding = "unknown"
	request.Owner = "operator:investigator"
	result, err := f.ops.Reconcile(ctx, request)
	if err != nil || result.State != "investigating" || result.ResolvedBy != nil {
		t.Fatalf("investigation: %+v %v", result, err)
	}
	queue, err := f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(queue.Items) != 1 || queue.Items[0].Case == nil || queue.Items[0].Case.Owner != request.Owner {
		t.Fatalf("ownership missing: %+v %v", queue, err)
	}
	request.Disposition = "redeliver"
	if _, err = f.ops.Reconcile(ctx, request); err == nil {
		t.Fatal("compensated unknown finding")
	}
}

func TestInvestigatingCaseStaysVisibleAfterDeviceAcknowledgement(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	id := f.credit.MeterCommand.CommandID
	if _, err := f.pool.Exec(ctx, `UPDATE meter_commands SET status='sent',requested_at=now()-interval '2 hours',expires_at=now()-interval '1 hour',sent_at=now()-interval '90 minutes' WHERE command_id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	request := f.decision("investigate")
	request.Finding = "unknown"
	request.Owner = "operator:investigator"
	if _, err := f.ops.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	queue, err := f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(queue.Items) != 1 || queue.Items[0].Reason() != domain.ReconciliationReason("case_open") {
		t.Fatalf("investigation vanished after delivery: %+v %v", queue, err)
	}
	request.Disposition = "delivered"
	request.Finding = "delivered"
	request.Evidence = "Device acknowledgement and ledger confirm delivery"
	if _, err = f.ops.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	queue, err = f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(queue.Items) != 0 {
		t.Fatalf("resolved delivery remains open: %+v %v", queue, err)
	}
}

func (f operationsFixture) investigate(t *testing.T, actor string) {
	t.Helper()
	r := f.decision(domain.DispositionInvestigate)
	r.Actor, r.Owner, r.Finding = actor, actor, "unknown"
	if _, err := f.ops.Reconcile(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestWriteOffIsRejectedAndUnknownCreditStaysInvestigating(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	f.investigate(t, "operator:second")
	for _, finding := range []domain.CaseFinding{domain.FindingUnknown, domain.FindingNotDelivered} {
		decision := f.decision("write_off")
		decision.Finding = finding
		if _, err := f.ops.Reconcile(ctx, decision); !errors.Is(err, services.ErrInvalidRequest) {
			t.Fatalf("write-off accepted for %s: %v", finding, err)
		}
	}
	listed, err := f.service.ListReconciliation(ctx, &f.siteID, 10, "")
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Case == nil || listed.Items[0].Case.State != domain.CaseInvestigating {
		t.Fatalf("uncertain credit hidden: %+v %v", listed, err)
	}
}

func TestQueuedReplacementStillSettlesAfterLateEvidence(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	decision, err := f.ops.Reconcile(ctx, f.decision("redeliver"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := *decision.ReplacementCommandID
	t.Cleanup(func() { cleaner(t, f.pool)(`DELETE FROM command_event_outbox WHERE aggregate_id=$1`, replacement) })
	if commands, err := f.service.Poll(ctx, f.siteID, 0); err != nil || !slices.ContainsFunc(commands, func(c domain.EdgeCommand) bool { return c.CommandID == replacement }) {
		t.Fatalf("initial dispatch: %+v %v", commands, err)
	}
	if _, _, err = f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: f.credit.MeterCommand.CommandID, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now()}); !errors.Is(err, services.ErrReportNeedsReconciliation) {
		t.Fatal(err)
	}
	commands, err := f.service.Poll(ctx, f.siteID, 0)
	if err != nil || !slices.ContainsFunc(commands, func(c domain.EdgeCommand) bool { return c.CommandID == replacement }) || f.status(t, replacement) != domain.CommandStatusQueued {
		t.Fatalf("already dispatched command was hidden instead of reconciled: %+v %v", commands, err)
	}
}

func TestReplacementPollWaitsForInFlightPositiveEvidence(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	decision, err := f.ops.Reconcile(ctx, f.decision("redeliver"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := *decision.ReplacementCommandID
	t.Cleanup(func() { cleaner(t, f.pool)(`DELETE FROM command_event_outbox WHERE aggregate_id=$1`, replacement) })
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	current, err := (edgeCommandTx{tx}).LockSiteCommand(ctx, f.siteID, f.credit.MeterCommand.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if err = (edgeCommandTx{tx}).RetainConflictingReport(ctx, f.siteID, current, domain.CommandReport{CommandID: current.CommandID, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	type pollResult struct {
		commands []domain.EdgeCommand
		err      error
	}
	done := make(chan pollResult, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		commands, err := f.service.Poll(ctx, f.siteID, 0)
		done <- pollResult{commands, err}
	}()
	<-started
	select {
	case result := <-done:
		t.Fatalf("poll bypassed in-flight delivery evidence: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || len(result.commands) != 0 {
			t.Fatalf("positive evidence did not hold replacement: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not finish after evidence committed")
	}
	if f.status(t, replacement) != domain.CommandStatusRequested {
		t.Fatal("held command history was rewritten")
	}
}

func TestRetiredWriteOffMigrationReopensAndAuditsExistingCases(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Recreate the preceding migration's policy only inside this rolled-back test.
	if _, err = tx.Exec(ctx, `ALTER TABLE command_reconciliation_cases DROP CONSTRAINT command_reconciliation_cases_disposition_check;
 ALTER TABLE command_reconciliation_cases ADD CONSTRAINT command_reconciliation_cases_disposition_check CHECK(disposition IN('investigate','delivered','redeliver','refund','write_off'))`); err != nil {
		t.Fatal(err)
	}
	id := f.credit.MeterCommand.CommandID
	if _, err = tx.Exec(ctx, `INSERT INTO command_reconciliation_cases(command_id,state,owner,finding,disposition,reason,evidence,resolved_by)
 VALUES($1::uuid,'resolved','operator:investigator','unknown','write_off','Historical decision','Original evidence','operator:approver')`, id); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "engine-rust", "migrations", "202610310001_retire_case_write_off.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	reopened, err := scanCase(tx.QueryRow(ctx, `SELECT `+caseColumns+` FROM command_reconciliation_cases WHERE command_id=$1::uuid`, id))
	if err != nil || reopened.State != domain.CaseInvestigating || reopened.Disposition != domain.DispositionInvestigate || reopened.ResolvedBy != nil {
		t.Fatalf("migration did not reopen case: %+v %v", reopened, err)
	}
	var previousDisposition string
	if err = tx.QueryRow(ctx, `SELECT metadata->'previous_case'->>'disposition' FROM audit_events WHERE subject_id=$1 AND action='meter_command.reconciliation.reopened'`, id).Scan(&previousDisposition); err != nil || previousDisposition != "write_off" {
		t.Fatalf("original decision not audited: %q %v", previousDisposition, err)
	}
	if _, err = tx.Exec(ctx, `UPDATE command_reconciliation_cases SET disposition='write_off' WHERE command_id=$1::uuid`, id); err == nil {
		t.Fatal("retired disposition still accepted by schema")
	}
}

func TestRetainedSentReportBlocksCompensationAndHoldsReplacement(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	id := f.credit.MeterCommand.CommandID
	replacement, err := f.ops.Reconcile(ctx, f.decision(domain.DispositionRedeliver))
	if err != nil {
		t.Fatal(err)
	}
	report := domain.CommandReport{CommandID: id, Status: domain.CommandStatusSent, OccurredAt: time.Now()}
	if _, _, err := f.service.Report(ctx, f.siteID, report); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("sent after expiry: %v", err)
	}
	pending, err := f.service.Poll(ctx, f.siteID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(pending, func(c domain.EdgeCommand) bool { return c.CommandID == *replacement.ReplacementCommandID }) {
		t.Fatal("replacement dispatched while an earlier attempt may have reached the device")
	}
}

func TestSentReportOnAnExpiredAttemptBlocksCompensation(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	report := domain.CommandReport{CommandID: f.credit.MeterCommand.CommandID, Status: domain.CommandStatusSent, OccurredAt: time.Now()}
	if _, _, err := f.service.Report(ctx, f.siteID, report); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("sent after expiry: %v", err)
	}
	refund := f.decision(domain.DispositionRefund)
	refund.RefundReference = "refund-" + f.siteID
	for _, decision := range []domain.ReconcileCommand{f.decision(domain.DispositionRedeliver), refund} {
		if _, err := f.ops.Reconcile(ctx, decision); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("%s ran on uncertain delivery: %v", decision.Disposition, err)
		}
	}
}

func TestDeliveredWithoutDeviceEvidenceNeedsASecondOperator(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	delivered := f.decision(domain.DispositionDelivered)
	delivered.Finding = domain.FindingDelivered
	if _, err := f.ops.Reconcile(ctx, delivered); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("delivered with no investigation: %v", err)
	}
	investigate := f.decision(domain.DispositionInvestigate)
	investigate.Finding, investigate.Owner = domain.FindingUnknown, delivered.Actor
	if _, err := f.ops.Reconcile(ctx, investigate); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ops.Reconcile(ctx, delivered); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("operator confirmed their own investigation: %v", err)
	}
	second := delivered
	second.Actor = "operator:second"
	if _, err := f.ops.Reconcile(ctx, second); err != nil {
		t.Fatalf("second operator refused: %v", err)
	}
}

func TestDeliveredCannotOverrideARefund(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	refund := f.decision(domain.DispositionRefund)
	refund.RefundReference = "refund-" + f.siteID
	if _, err := f.ops.Reconcile(ctx, refund); err != nil {
		t.Fatal(err)
	}
	delivered := f.decision(domain.DispositionDelivered)
	delivered.Finding = domain.FindingDelivered
	if _, err := f.ops.Reconcile(ctx, delivered); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("refunded case changed to delivered: %v", err)
	}
}

func TestReconnectRefusedWhenThePlatformBalanceIsExhausted(t *testing.T) {
	f := newOperationsFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE smart_meters SET credit_mode='platform' WHERE meter_id=$1`, f.meterID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE credit_balances SET remaining_kwh=0,remaining_money_value_minor_units=0 WHERE assignment_id=$1::uuid`, f.credit.Credit.AssignmentID); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	request := domain.CreateMeterCommand{RequestID: id, MeterID: f.meterID, Type: domain.CommandReconnectMeter, ConfirmMeterID: f.meterID, Reason: "Field request", Actor: "operator:test"}
	if _, err := f.ops.Create(ctx, request); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reconnected an exhausted balance: %v", err)
	}
	request.Type = domain.CommandDisconnectMeter
	if _, err := f.ops.Create(ctx, request); err != nil {
		t.Fatalf("disconnect refused: %v", err)
	}
}
