package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"api-go/internal/domain"
	"api-go/internal/services"
)

func paymentRow(command domain.ApplyPaymentCommand) pgx.Row {
	return fakeRow(func(dest ...any) error {
		confirmedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		createdAt := confirmedAt
		*dest[0].(*string) = "pay-001"
		*dest[1].(*string) = command.Provider
		*dest[2].(*string) = command.ExternalReference
		*dest[3].(*string) = command.CustomerID
		*dest[4].(*int64) = command.AmountMinorUnits
		*dest[5].(*string) = command.Currency
		*dest[6].(*domain.PaymentStatus) = domain.PaymentStatusConfirmed
		*dest[7].(**time.Time) = &confirmedAt
		*dest[8].(*time.Time) = createdAt
		return nil
	})
}

func assignmentRow(assignmentID, siteID, meterID string, relayClosed bool) pgx.Row {
	return fakeRow(func(dest ...any) error {
		*dest[0].(*string) = assignmentID
		*dest[1].(*string) = siteID
		*dest[2].(*string) = meterID
		*dest[3].(*bool) = relayClosed
		return nil
	})
}

func tariffRow(tariffPlanID string, pricePerKWh float64, currency string, minorUnitsPerMajor int) pgx.Row {
	return fakeRow(func(dest ...any) error {
		*dest[0].(*string) = tariffPlanID
		*dest[1].(*float64) = pricePerKWh
		*dest[2].(*string) = currency
		*dest[3].(*int) = minorUnitsPerMajor
		return nil
	})
}

func creditRow(siteID, assignmentID string, kwhGranted float64, moneyMinorUnits int64) pgx.Row {
	return fakeRow(func(dest ...any) error {
		paymentID := "pay-001"
		tariffPlanID := "tariff-01"
		createdAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		*dest[0].(*string) = "credit-001"
		*dest[1].(*string) = siteID
		*dest[2].(*string) = assignmentID
		*dest[3].(**string) = &paymentID
		*dest[4].(**string) = &tariffPlanID
		*dest[5].(*domain.CreditSourceType) = domain.CreditSourcePayment
		*dest[6].(*string) = "pay-001"
		*dest[7].(*float64) = kwhGranted
		*dest[8].(**int64) = &moneyMinorUnits
		*dest[9].(*time.Time) = createdAt
		return nil
	})
}

func priorBalanceRow(kwh float64) pgx.Row {
	return fakeRow(func(dest ...any) error {
		*dest[0].(*float64) = kwh
		return nil
	})
}

func balanceRow(assignmentID string, remainingKWh float64, remainingMoney int64) pgx.Row {
	return fakeRow(func(dest ...any) error {
		updatedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		*dest[0].(*string) = assignmentID
		*dest[1].(*float64) = remainingKWh
		*dest[2].(*int64) = remainingMoney
		*dest[3].(*time.Time) = updatedAt
		return nil
	})
}

func meterCommandRow(meterID string) pgx.Row {
	return fakeRow(func(dest ...any) error {
		requestedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		*dest[0].(*string) = "cmd-001"
		*dest[1].(*string) = meterID
		*dest[2].(*domain.MeterCommandType) = domain.CommandReconnectMeter
		*dest[3].(*domain.MeterCommandStatus) = domain.CommandStatusRequested
		*dest[4].(*string) = "provider:dev"
		*dest[5].(*string) = "balance recharged from zero"
		*dest[6].(*time.Time) = requestedAt
		*dest[7].(**time.Time) = nil
		*dest[8].(**time.Time) = nil
		*dest[9].(**string) = nil
		return nil
	})
}

func testCommand() domain.ApplyPaymentCommand {
	return domain.ApplyPaymentCommand{
		Provider:          "dev",
		ExternalReference: "dev-abc123",
		CustomerID:        "cust-01",
		AmountMinorUnits:  500000,
		Currency:          "NGN",
		Status:            domain.PaymentStatusConfirmed,
		ConfirmedAt:       time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

func applyPaymentCallback(command domain.ApplyPaymentCommand) func(services.PaymentTx) (domain.ApplyPaymentResult, error) {
	return func(tx services.PaymentTx) (domain.ApplyPaymentResult, error) {
		ctx := context.Background()

		existing, err := tx.FindExistingPayment(ctx, command.Provider, command.ExternalReference)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}
		if existing != nil {
			return tx.ReplayPriorResult(ctx, *existing)
		}

		payment, err := tx.InsertPayment(ctx, command)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}
		if payment == nil {
			existing, err := tx.FindExistingPayment(ctx, command.Provider, command.ExternalReference)
			if err != nil {
				return domain.ApplyPaymentResult{}, err
			}
			if existing == nil {
				return domain.ApplyPaymentResult{}, errors.New("payment conflict winner was not visible")
			}
			return tx.ReplayPriorResult(ctx, *existing)
		}

		assignment, err := tx.FindActiveAssignment(ctx, command.CustomerID)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		tariff, err := tx.FindActiveTariff(ctx, assignment.SiteID, command.ConfirmedAt)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		pricePerKWhMinor := tariff.PricePerKWh * float64(tariff.MinorUnitsPerMajor)
		kwhGranted := float64(command.AmountMinorUnits) / pricePerKWhMinor

		credit, err := tx.InsertEnergyCredit(ctx, assignment.SiteID, assignment.AssignmentID, payment.PaymentID, tariff.TariffPlanID, kwhGranted, command.AmountMinorUnits)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		priorKWh, err := tx.GetPriorBalance(ctx, assignment.AssignmentID)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		balance, err := tx.UpsertCreditBalance(ctx, assignment.AssignmentID, kwhGranted, command.AmountMinorUnits)
		if err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		if err := tx.InsertPaymentConfirmedEvent(ctx, *payment); err != nil {
			return domain.ApplyPaymentResult{}, err
		}
		if err := tx.InsertCreditIssuedEvent(ctx, credit); err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		var meterCommand *domain.MeterCommand
		needsReconnect := !assignment.RelayClosed && priorKWh == 0 && balance.RemainingKWh > 0
		if needsReconnect {
			cmd, err := tx.InsertMeterCommand(ctx, assignment.MeterID, domain.CommandReconnectMeter, "provider:"+command.Provider, "balance recharged from zero")
			if err != nil {
				return domain.ApplyPaymentResult{}, err
			}
			if err := tx.InsertMeterCommandEvent(ctx, cmd, assignment.SiteID, assignment.AssignmentID); err != nil {
				return domain.ApplyPaymentResult{}, err
			}
			meterCommand = &cmd
		}

		auditMetadata := map[string]any{
			"payment_id":     payment.PaymentID,
			"credit_id":      credit.CreditID,
			"assignment_id":  assignment.AssignmentID,
			"kwh_granted":    kwhGranted,
			"amount":         command.AmountMinorUnits,
			"currency":       command.Currency,
			"tariff_plan_id": tariff.TariffPlanID,
			"remaining_kwh":  balance.RemainingKWh,
		}
		if meterCommand != nil {
			auditMetadata["meter_command_id"] = meterCommand.CommandID
			auditMetadata["meter_command_type"] = meterCommand.CommandType
		}
		if err := tx.InsertAuditEvent(ctx, assignment.SiteID, "provider:"+command.Provider, "payment.applied", "payment", payment.PaymentID, auditMetadata); err != nil {
			return domain.ApplyPaymentResult{}, err
		}

		return domain.ApplyPaymentResult{
			Payment:      *payment,
			Credit:       credit,
			Balance:      balance,
			MeterCommand: meterCommand,
		}, nil
	}
}

func TestRunPaymentTxCommitsTheFullChain(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{rows: []pgx.Row{
		errorRow(pgx.ErrNoRows), // no existing payment
		paymentRow(command),
		assignmentRow("assign-001", "site-01", "met-0101", false),
		tariffRow("tariff-01", 250, "NGN", 100),
		creditRow("site-01", "assign-001", 20.0, 500000),
		priorBalanceRow(0),
		balanceRow("assign-001", 20.0, 500000),
		meterCommandRow("met-0101"),
	}}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	result, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))
	if err != nil {
		t.Fatalf("RunPaymentTx() error = %v", err)
	}
	if pool.options.IsoLevel != pgx.ReadCommitted {
		t.Fatalf("isolation = %v, want read committed", pool.options.IsoLevel)
	}
	if result.Payment.PaymentID != "pay-001" || result.Payment.Provider != "dev" {
		t.Fatalf("payment = %+v", result.Payment)
	}
	if result.Credit.CreditID != "credit-001" || result.Credit.KWhGranted != 20.0 {
		t.Fatalf("credit = %+v", result.Credit)
	}
	if result.Balance.RemainingKWh != 20.0 {
		t.Fatalf("balance = %+v", result.Balance)
	}
	if result.MeterCommand == nil || result.MeterCommand.CommandType != domain.CommandReconnectMeter {
		t.Fatalf("meter_command = %+v", result.MeterCommand)
	}
	if !tx.committed {
		t.Fatal("transaction was not committed")
	}

	if len(tx.execCalls) != 4 {
		t.Fatalf("exec calls = %d, want 4 (three outbox events + audit event)", len(tx.execCalls))
	}

	paymentEvent := tx.execCalls[0]
	if paymentEvent.sql != insertCommandEventSQL || paymentEvent.args[0] != "payment.confirmed" || paymentEvent.args[1] != "payment.confirmed" || paymentEvent.args[2] != "payment" || paymentEvent.args[3] != "pay-001" {
		t.Fatalf("payment event args = %#v", paymentEvent.args)
	}

	creditEvent := tx.execCalls[1]
	if creditEvent.args[0] != "credit.issued" || creditEvent.args[1] != "credit.issued" || creditEvent.args[2] != "energy_credit" || creditEvent.args[3] != "credit-001" {
		t.Fatalf("credit event args = %#v", creditEvent.args)
	}

	outbox := tx.execCalls[2]
	if outbox.args[0] != "meter.command.requested" || outbox.args[1] != "meter.command.requested" || outbox.args[2] != "meter_command" || outbox.args[3] != "cmd-001" {
		t.Fatalf("meter command event args = %#v", outbox.args)
	}
	var outboxPayload map[string]any
	if err := json.Unmarshal([]byte(outbox.args[4].(string)), &outboxPayload); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	if outboxPayload["command_id"] != "cmd-001" || outboxPayload["meter_id"] != "met-0101" || outboxPayload["command_type"] != "reconnect_meter" {
		t.Fatalf("outbox payload = %v", outboxPayload)
	}

	audit := tx.execCalls[3]
	if audit.sql != insertAuditEventSQL {
		t.Fatalf("audit SQL = %q, want insertAuditEventSQL", audit.sql)
	}
	if audit.args[0] != "site-01" || audit.args[2] != "payment.applied" || audit.args[3] != "payment" || audit.args[4] != "pay-001" {
		t.Fatalf("audit args = %#v", audit.args)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(audit.args[5].(string)), &metadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	if metadata["payment_id"] != "pay-001" || metadata["meter_command_id"] != "cmd-001" {
		t.Fatalf("audit metadata = %v", metadata)
	}
}

func TestRunPaymentTxRollsBackOnCallbackError(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{rows: []pgx.Row{
		errorRow(pgx.ErrNoRows),
		paymentRow(command),
		errorRow(pgx.ErrNoRows), // no assignment
	}}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	_, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if tx.committed {
		t.Fatal("failed transaction was committed")
	}
}

func TestRunPaymentTxRollsBackWhenAuditInsertFails(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{
		rows: []pgx.Row{
			errorRow(pgx.ErrNoRows),
			paymentRow(command),
			assignmentRow("assign-001", "site-01", "met-0101", true),
			tariffRow("tariff-01", 250, "NGN", 100),
			creditRow("site-01", "assign-001", 20.0, 500000),
			priorBalanceRow(5.0),
			balanceRow("assign-001", 25.0, 1000000),
		},
		execErr: errors.New("audit unavailable"),
	}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	_, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))

	if err == nil {
		t.Fatal("RunPaymentTx() error = nil, want audit insert error")
	}
	if tx.committed {
		t.Fatal("transaction with failed audit insert was committed")
	}
	if !tx.rolledBack {
		t.Fatal("transaction with failed audit insert was not rolled back")
	}
}

func TestRunPaymentTxRollsBackWhenReconnectCommandFails(t *testing.T) {
	command := testCommand()
	tx := &paymentReconnectFailTransaction{
		fakeTransaction: &fakeTransaction{
			rows: []pgx.Row{
				errorRow(pgx.ErrNoRows),
				paymentRow(command),
				assignmentRow("assign-001", "site-01", "met-0101", false),
				tariffRow("tariff-01", 250, "NGN", 100),
				creditRow("site-01", "assign-001", 20.0, 500000),
				priorBalanceRow(0),
				balanceRow("assign-001", 20.0, 500000),
			},
		},
	}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	_, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))

	if err == nil {
		t.Fatal("RunPaymentTx() error = nil, want reconnect command error")
	}
	if tx.committed {
		t.Fatal("transaction with failed reconnect was committed")
	}
}

type paymentReconnectFailTransaction struct {
	*fakeTransaction
	queryRowCalls int
}

func (tx *paymentReconnectFailTransaction) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.queryRowCalls++
	// The 8th QueryRow call is the reconnect command insert
	if tx.queryRowCalls == 8 {
		return errorRow(errors.New("meter_commands insert failed"))
	}
	return tx.fakeTransaction.QueryRow(ctx, sql, args...)
}

func (tx *paymentReconnectFailTransaction) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return tx.fakeTransaction.Exec(ctx, sql, args...)
}

func (tx *paymentReconnectFailTransaction) Commit(ctx context.Context) error {
	return tx.fakeTransaction.Commit(ctx)
}

func (tx *paymentReconnectFailTransaction) Rollback(ctx context.Context) error {
	return tx.fakeTransaction.Rollback(ctx)
}

func TestRunPaymentTxRollsBackWhenOutboxInsertFails(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{
		rows: []pgx.Row{
			errorRow(pgx.ErrNoRows),
			paymentRow(command),
			assignmentRow("assign-001", "site-01", "met-0101", false),
			tariffRow("tariff-01", 250, "NGN", 100),
			creditRow("site-01", "assign-001", 20.0, 500000),
			priorBalanceRow(0),
			balanceRow("assign-001", 20.0, 500000),
			meterCommandRow("met-0101"),
		},
		execErr: errors.New("outbox unavailable"),
	}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	_, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))

	if err == nil {
		t.Fatal("RunPaymentTx() error = nil, want outbox insert error")
	}
	if tx.committed {
		t.Fatal("transaction with failed outbox insert was committed")
	}
	if !tx.rolledBack {
		t.Fatal("transaction with failed outbox insert was not rolled back")
	}
}

func TestRunPaymentTxReplaysDuplicateWithConsistentPayload(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{rows: []pgx.Row{
		paymentRow(command), // existing payment found
		creditRow("site-01", "assign-001", 20.0, 500000),
		balanceRow("assign-001", 20.0, 500000),
		meterCommandRow("met-0101"),
	}}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	result, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))
	if err != nil {
		t.Fatalf("RunPaymentTx() error = %v", err)
	}
	if result.Payment.PaymentID != "pay-001" {
		t.Fatalf("payment_id = %v, want pay-001", result.Payment.PaymentID)
	}
	if result.Credit.CreditID != "credit-001" {
		t.Fatalf("credit_id = %v, want credit-001", result.Credit.CreditID)
	}
	if result.Balance.RemainingKWh != 20.0 {
		t.Fatalf("remaining_kwh = %v, want 20.0", result.Balance.RemainingKWh)
	}
	if result.MeterCommand == nil || result.MeterCommand.CommandID != "cmd-001" {
		t.Fatalf("meter_command = %+v, want cmd-001", result.MeterCommand)
	}
	if len(tx.execCalls) != 0 {
		t.Fatalf("exec calls = %d, want 0 (no audit event on replay)", len(tx.execCalls))
	}
}

func TestRunPaymentTxReplaysDuplicateWithoutMeterCommand(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{rows: []pgx.Row{
		paymentRow(command),
		creditRow("site-01", "assign-001", 20.0, 500000),
		balanceRow("assign-001", 20.0, 500000),
		errorRow(pgx.ErrNoRows),
	}}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	result, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))
	if err != nil {
		t.Fatalf("RunPaymentTx() error = %v", err)
	}
	if result.MeterCommand != nil {
		t.Fatalf("meter_command = %+v, want nil on replay without reconnect", result.MeterCommand)
	}
}

func TestRunPaymentTxReplaysConcurrentDuplicateAfterInsertConflict(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{rows: []pgx.Row{
		errorRow(pgx.ErrNoRows),                          // no existing payment (race)
		errorRow(pgx.ErrNoRows),                          // ON CONFLICT DO NOTHING returns no row
		paymentRow(command),                              // re-fetch finds the winner
		creditRow("site-01", "assign-001", 20.0, 500000), // replay credit
		balanceRow("assign-001", 20.0, 500000),           // replay balance
		errorRow(pgx.ErrNoRows),                          // no meter command
	}}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	result, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))
	if err != nil {
		t.Fatalf("RunPaymentTx() error = %v", err)
	}
	if result.Payment.PaymentID != "pay-001" {
		t.Fatalf("payment_id = %v, want pay-001", result.Payment.PaymentID)
	}
	if result.Credit.CreditID != "credit-001" {
		t.Fatalf("credit_id = %v, want credit-001", result.Credit.CreditID)
	}
	if result.Balance.RemainingKWh != 20.0 {
		t.Fatalf("remaining_kwh = %v, want 20.0", result.Balance.RemainingKWh)
	}
	if result.MeterCommand != nil {
		t.Fatalf("meter_command = %+v, want nil", result.MeterCommand)
	}
	if len(tx.execCalls) != 0 {
		t.Fatalf("exec calls = %d, want 0 (no side effects on replay)", len(tx.execCalls))
	}
}

func TestFindActiveTariffReturnsCurrency(t *testing.T) {
	tx := &fakeTransaction{rows: []pgx.Row{
		tariffRow("tariff-01", 250, "KES", 100),
	}}

	tariff, err := (&paymentTx{tx: tx}).FindActiveTariff(
		context.Background(),
		"site-01",
		time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("FindActiveTariff() error = %v", err)
	}
	if tariff.Currency != "KES" {
		t.Fatalf("currency = %q, want KES", tariff.Currency)
	}
}

func TestFindActiveAssignmentReturnsNotFoundForMissingAssignment(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{rows: []pgx.Row{
		errorRow(pgx.ErrNoRows),
		paymentRow(command),
		errorRow(pgx.ErrNoRows),
	}}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	_, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if tx.committed {
		t.Fatal("failed transaction was committed")
	}
}

func TestFindActiveTariffReturnsNotFoundForMissingTariff(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{rows: []pgx.Row{
		errorRow(pgx.ErrNoRows),
		paymentRow(command),
		assignmentRow("assign-001", "site-01", "met-0101", true),
		errorRow(pgx.ErrNoRows),
	}}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	_, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if tx.committed {
		t.Fatal("failed transaction was committed")
	}
}

func TestGetPriorBalancePropagatesError(t *testing.T) {
	command := testCommand()
	tx := &fakeTransaction{rows: []pgx.Row{
		errorRow(pgx.ErrNoRows),
		paymentRow(command),
		assignmentRow("assign-001", "site-01", "met-0101", false),
		tariffRow("tariff-01", 250, "NGN", 100),
		creditRow("site-01", "assign-001", 20.0, 500000),
		errorRow(errors.New("connection lost")),
	}}
	pool := &fakeDatabasePool{tx: tx}
	store := NewStore(pool, time.Second)

	_, err := store.RunPaymentTx(context.Background(), applyPaymentCallback(command))
	if err == nil {
		t.Fatal("RunPaymentTx() error = nil, want prior balance error")
	}
	if tx.committed {
		t.Fatal("transaction with failed balance lookup was committed")
	}
}
