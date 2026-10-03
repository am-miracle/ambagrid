package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"api-go/internal/domain"
	"api-go/internal/services"
)

const findExistingPaymentSQL = `
SELECT payment_id::text, provider, external_reference, customer_id,
    amount_minor_units, currency, status, confirmed_at, created_at
FROM payments
WHERE provider = $1 AND external_reference = $2`

const insertPaymentSQL = `
INSERT INTO payments (
    provider, external_reference, customer_id,
    amount_minor_units, currency, status, confirmed_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (provider, external_reference) DO NOTHING
RETURNING payment_id::text, provider, external_reference, customer_id,
    amount_minor_units, currency, status, confirmed_at, created_at`

const findCreditByPaymentSQL = `
SELECT credit_id::text, site_id, assignment_id::text, payment_id::text,
    tariff_plan_id, source_type, source_id, kwh_granted,
    money_value_minor_units, created_at
FROM energy_credits
WHERE payment_id = $1::uuid`

const findBalanceByAssignmentSQL = `
SELECT assignment_id::text, remaining_kwh, remaining_money_value_minor_units, updated_at
FROM credit_balances
WHERE assignment_id = $1::uuid`

const findMeterCommandByPaymentSQL = `
SELECT mc.command_id::text, mc.meter_id, mc.command_type, mc.status,
    mc.requested_by, mc.reason, mc.requested_at, mc.sent_at,
    mc.acknowledged_at, mc.failure_reason
FROM meter_commands mc
JOIN audit_events ae ON ae.metadata->>'meter_command_id' = mc.command_id::text
WHERE ae.subject_type = 'payment' AND ae.subject_id = $1`

// Missing telemetry means the relay state is unknown. Treat it as closed to
// avoid issuing speculative reconnects for every newly onboarded meter.
const findActiveAssignmentSQL = `
SELECT ma.assignment_id::text, ma.site_id, ma.meter_id,
    COALESCE(sms.relay_closed, true)
FROM meter_assignments ma
JOIN smart_meters sm ON sm.meter_id = ma.meter_id
LEFT JOIN smart_meter_state sms ON sms.asset_id = sm.asset_id
WHERE ma.customer_id = $1
  AND ma.ended_at IS NULL
ORDER BY ma.started_at DESC
LIMIT 1`

const findActiveTariffSQL = `
SELECT tariff_plan_id, price_per_kwh, currency, minor_units_per_major
FROM tariff_plans
WHERE site_id = $1
  AND effective_from <= $2
  AND (effective_to IS NULL OR effective_to > $2)
ORDER BY effective_from DESC
LIMIT 1`

const insertEnergyCreditSQL = `
INSERT INTO energy_credits (
    site_id, assignment_id, payment_id, tariff_plan_id,
    source_type, source_id, kwh_granted, money_value_minor_units
)
VALUES ($1, $2::uuid, $3::uuid, $4, 'payment', $5, $6, $7)
RETURNING credit_id::text, site_id, assignment_id::text, payment_id::text,
    tariff_plan_id, source_type, source_id, kwh_granted,
    money_value_minor_units, created_at`

const upsertCreditBalanceSQL = `
INSERT INTO credit_balances (assignment_id, remaining_kwh, remaining_money_value_minor_units)
VALUES ($1::uuid, $2, $3)
ON CONFLICT (assignment_id) DO UPDATE SET
    remaining_kwh = credit_balances.remaining_kwh + EXCLUDED.remaining_kwh,
    remaining_money_value_minor_units = credit_balances.remaining_money_value_minor_units + EXCLUDED.remaining_money_value_minor_units
RETURNING assignment_id::text, remaining_kwh, remaining_money_value_minor_units, updated_at`

const getPriorBalanceSQL = `
SELECT remaining_kwh FROM credit_balances
WHERE assignment_id = $1::uuid`

const insertMeterCommandSQL = `
INSERT INTO meter_commands (
    meter_id, command_type, status, requested_by, reason
)
VALUES ($1, $2, 'requested', $3, $4)
RETURNING command_id::text, meter_id, command_type, status,
    requested_by, reason, requested_at, sent_at, acknowledged_at, failure_reason`

const insertAuditEventSQL = `
INSERT INTO audit_events (
    site_id, actor_id, action, subject_type, subject_id, metadata
)
VALUES ($1, $2, $3, $4, $5, $6::jsonb)`

type paymentTx struct {
	tx           pgx.Tx
	outboxTopics OutboxTopics
}

func (s *Store) RunPaymentTx(ctx context.Context, fn func(services.PaymentTx) (domain.ApplyPaymentResult, error)) (domain.ApplyPaymentResult, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return domain.ApplyPaymentResult{}, fmt.Errorf("begin payment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ptx := &paymentTx{tx: tx, outboxTopics: s.outboxTopics}
	result, err := fn(ptx)
	if err != nil {
		return domain.ApplyPaymentResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.ApplyPaymentResult{}, fmt.Errorf("commit payment transaction: %w", err)
	}

	return result, nil
}

func (p *paymentTx) FindExistingPayment(ctx context.Context, provider, externalReference string) (*domain.Payment, error) {
	var payment domain.Payment
	err := p.tx.QueryRow(ctx, findExistingPaymentSQL, provider, externalReference).Scan(
		&payment.PaymentID,
		&payment.Provider,
		&payment.ExternalReference,
		&payment.CustomerID,
		&payment.AmountMinorUnits,
		&payment.Currency,
		&payment.Status,
		&payment.ConfirmedAt,
		&payment.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find existing payment: %w", err)
	}
	return &payment, nil
}

func (p *paymentTx) InsertPayment(ctx context.Context, command domain.ApplyPaymentCommand) (*domain.Payment, error) {
	var payment domain.Payment
	err := p.tx.QueryRow(ctx, insertPaymentSQL,
		command.Provider,
		command.ExternalReference,
		command.CustomerID,
		command.AmountMinorUnits,
		command.Currency,
		command.Status,
		command.ConfirmedAt,
	).Scan(
		&payment.PaymentID,
		&payment.Provider,
		&payment.ExternalReference,
		&payment.CustomerID,
		&payment.AmountMinorUnits,
		&payment.Currency,
		&payment.Status,
		&payment.ConfirmedAt,
		&payment.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("insert payment: %w", err)
	}
	return &payment, nil
}

func (p *paymentTx) ReplayPriorResult(ctx context.Context, payment domain.Payment) (domain.ApplyPaymentResult, error) {
	var credit domain.EnergyCredit
	err := p.tx.QueryRow(ctx, findCreditByPaymentSQL, payment.PaymentID).Scan(
		&credit.CreditID,
		&credit.SiteID,
		&credit.AssignmentID,
		&credit.PaymentID,
		&credit.TariffPlanID,
		&credit.SourceType,
		&credit.SourceID,
		&credit.KWhGranted,
		&credit.MoneyValueMinorUnits,
		&credit.CreatedAt,
	)
	if err != nil {
		return domain.ApplyPaymentResult{}, fmt.Errorf("replay credit lookup: %w", err)
	}

	var balance domain.CreditBalance
	err = p.tx.QueryRow(ctx, findBalanceByAssignmentSQL, credit.AssignmentID).Scan(
		&balance.AssignmentID,
		&balance.RemainingKWh,
		&balance.RemainingMoneyValueMinorUnits,
		&balance.UpdatedAt,
	)
	if err != nil {
		return domain.ApplyPaymentResult{}, fmt.Errorf("replay balance lookup: %w", err)
	}

	var meterCommand *domain.MeterCommand
	var cmd domain.MeterCommand
	err = p.tx.QueryRow(ctx, findMeterCommandByPaymentSQL, payment.PaymentID).Scan(
		&cmd.CommandID,
		&cmd.MeterID,
		&cmd.CommandType,
		&cmd.Status,
		&cmd.RequestedBy,
		&cmd.Reason,
		&cmd.RequestedAt,
		&cmd.SentAt,
		&cmd.AcknowledgedAt,
		&cmd.FailureReason,
	)
	if err == nil {
		meterCommand = &cmd
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ApplyPaymentResult{}, fmt.Errorf("replay meter command lookup: %w", err)
	}

	return domain.ApplyPaymentResult{
		Payment:      payment,
		Credit:       credit,
		Balance:      balance,
		MeterCommand: meterCommand,
	}, nil
}

func (p *paymentTx) FindActiveAssignment(ctx context.Context, customerID string) (domain.MeterAssignment, error) {
	var a domain.MeterAssignment
	err := p.tx.QueryRow(ctx, findActiveAssignmentSQL, customerID).Scan(&a.AssignmentID, &a.SiteID, &a.MeterID, &a.RelayClosed)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MeterAssignment{}, fmt.Errorf("%w: customer %q has no active meter assignment", domain.ErrNotFound, customerID)
	}
	if err != nil {
		return domain.MeterAssignment{}, fmt.Errorf("find active assignment: %w", err)
	}
	return a, nil
}

func (p *paymentTx) FindActiveTariff(ctx context.Context, siteID string, at time.Time) (domain.Tariff, error) {
	var t domain.Tariff
	err := p.tx.QueryRow(ctx, findActiveTariffSQL, siteID, at).Scan(&t.TariffPlanID, &t.PricePerKWh, &t.Currency, &t.MinorUnitsPerMajor)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Tariff{}, fmt.Errorf("%w: no active tariff plan for site %q", domain.ErrNotFound, siteID)
	}
	if err != nil {
		return domain.Tariff{}, fmt.Errorf("find active tariff: %w", err)
	}
	if t.PricePerKWh <= 0 {
		return domain.Tariff{}, fmt.Errorf("%w: tariff %q has non-positive price_per_kwh", domain.ErrInvalidData, t.TariffPlanID)
	}
	if t.MinorUnitsPerMajor <= 0 {
		return domain.Tariff{}, fmt.Errorf("%w: tariff %q has non-positive minor_units_per_major", domain.ErrInvalidData, t.TariffPlanID)
	}
	return t, nil
}

func (p *paymentTx) InsertEnergyCredit(ctx context.Context, siteID, assignmentID, paymentID, tariffPlanID string, kwhGranted float64, moneyMinorUnits int64) (domain.EnergyCredit, error) {
	var credit domain.EnergyCredit
	err := p.tx.QueryRow(ctx, insertEnergyCreditSQL,
		siteID, assignmentID, paymentID, tariffPlanID,
		paymentID, kwhGranted, moneyMinorUnits,
	).Scan(
		&credit.CreditID,
		&credit.SiteID,
		&credit.AssignmentID,
		&credit.PaymentID,
		&credit.TariffPlanID,
		&credit.SourceType,
		&credit.SourceID,
		&credit.KWhGranted,
		&credit.MoneyValueMinorUnits,
		&credit.CreatedAt,
	)
	if err != nil {
		return domain.EnergyCredit{}, fmt.Errorf("insert energy credit: %w", err)
	}
	return credit, nil
}

func (p *paymentTx) GetPriorBalance(ctx context.Context, assignmentID string) (float64, error) {
	var kwh float64
	err := p.tx.QueryRow(ctx, getPriorBalanceSQL, assignmentID).Scan(&kwh)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get prior balance: %w", err)
	}
	return kwh, nil
}

func (p *paymentTx) UpsertCreditBalance(ctx context.Context, assignmentID string, kwhGranted float64, moneyMinorUnits int64) (domain.CreditBalance, error) {
	var balance domain.CreditBalance
	err := p.tx.QueryRow(ctx, upsertCreditBalanceSQL,
		assignmentID, kwhGranted, moneyMinorUnits,
	).Scan(
		&balance.AssignmentID,
		&balance.RemainingKWh,
		&balance.RemainingMoneyValueMinorUnits,
		&balance.UpdatedAt,
	)
	if err != nil {
		return domain.CreditBalance{}, fmt.Errorf("upsert credit balance: %w", err)
	}
	return balance, nil
}

func (p *paymentTx) InsertMeterCommand(ctx context.Context, meterID string, commandType domain.MeterCommandType, requestedBy, reason string) (domain.MeterCommand, error) {
	var cmd domain.MeterCommand
	err := p.tx.QueryRow(ctx, insertMeterCommandSQL,
		meterID, commandType, requestedBy, reason,
	).Scan(
		&cmd.CommandID,
		&cmd.MeterID,
		&cmd.CommandType,
		&cmd.Status,
		&cmd.RequestedBy,
		&cmd.Reason,
		&cmd.RequestedAt,
		&cmd.SentAt,
		&cmd.AcknowledgedAt,
		&cmd.FailureReason,
	)
	if err != nil {
		return domain.MeterCommand{}, fmt.Errorf("insert meter command: %w", err)
	}
	return cmd, nil
}

func (p *paymentTx) InsertMeterCommandEvent(ctx context.Context, cmd domain.MeterCommand, siteID, assignmentID string) error {
	payload, err := json.Marshal(map[string]any{
		"command_id":    cmd.CommandID,
		"meter_id":      cmd.MeterID,
		"command_type":  cmd.CommandType,
		"status":        cmd.Status,
		"requested_by":  cmd.RequestedBy,
		"reason":        cmd.Reason,
		"site_id":       siteID,
		"assignment_id": assignmentID,
		"requested_at":  cmd.RequestedAt,
	})
	if err != nil {
		return fmt.Errorf("marshal meter command event: %w", err)
	}
	if _, err := p.tx.Exec(ctx, insertCommandEventSQL, p.outboxTopics.MeterCommandRequested, "meter.command.requested", "meter_command", cmd.CommandID, string(payload)); err != nil {
		return fmt.Errorf("insert meter command event: %w", err)
	}
	return nil
}

func (p *paymentTx) InsertPaymentConfirmedEvent(ctx context.Context, payment domain.Payment) error {
	payload, err := json.Marshal(map[string]any{
		"payment_id":         payment.PaymentID,
		"provider":           payment.Provider,
		"external_reference": payment.ExternalReference,
		"customer_id":        payment.CustomerID,
		"amount_minor_units": payment.AmountMinorUnits,
		"currency":           payment.Currency,
		"confirmed_at":       payment.ConfirmedAt,
	})
	if err != nil {
		return fmt.Errorf("marshal payment confirmed event: %w", err)
	}
	if _, err := p.tx.Exec(ctx, insertCommandEventSQL, p.outboxTopics.PaymentConfirmed, "payment.confirmed", "payment", payment.PaymentID, string(payload)); err != nil {
		return fmt.Errorf("insert payment confirmed event: %w", err)
	}
	return nil
}

func (p *paymentTx) InsertCreditIssuedEvent(ctx context.Context, credit domain.EnergyCredit) error {
	payload, err := json.Marshal(map[string]any{
		"credit_id":               credit.CreditID,
		"site_id":                 credit.SiteID,
		"assignment_id":           credit.AssignmentID,
		"payment_id":              credit.PaymentID,
		"tariff_plan_id":          credit.TariffPlanID,
		"source_type":             credit.SourceType,
		"source_id":               credit.SourceID,
		"kwh_granted":             credit.KWhGranted,
		"money_value_minor_units": credit.MoneyValueMinorUnits,
		"created_at":              credit.CreatedAt,
	})
	if err != nil {
		return fmt.Errorf("marshal credit issued event: %w", err)
	}
	if _, err := p.tx.Exec(ctx, insertCommandEventSQL, p.outboxTopics.CreditIssued, "credit.issued", "energy_credit", credit.CreditID, string(payload)); err != nil {
		return fmt.Errorf("insert credit issued event: %w", err)
	}
	return nil
}

func (p *paymentTx) InsertAuditEvent(ctx context.Context, siteID, actorID, action, subjectType, subjectID string, metadata map[string]any) error {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	if _, err := p.tx.Exec(ctx, insertAuditEventSQL, siteID, actorID, action, subjectType, subjectID, string(metadataJSON)); err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}
