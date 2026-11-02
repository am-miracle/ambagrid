package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"api-go/internal/domain"
	"api-go/internal/services"
)

const caseColumns = `command_id::text,state,needs_review,owner,finding,disposition,reason,evidence,resolved_by,replacement_command_id::text,refund_reference,updated_at`

func scanCase(row scanner) (domain.ReconciliationCase, error) {
	var c domain.ReconciliationCase
	err := row.Scan(&c.CommandID, &c.State, &c.NeedsReview, &c.Owner, &c.Finding, &c.Disposition, &c.Reason, &c.Evidence, &c.ResolvedBy, &c.ReplacementCommandID, &c.RefundReference, &c.UpdatedAt)
	return c, err
}

type meterOperationsTx struct {
	tx      pgx.Tx
	payment paymentTx
}

func (s *Store) RunMeterOperationsTx(ctx context.Context, fn func(services.MeterOperationsTx) error) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin meter operations transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(meterOperationsTx{tx: tx, payment: paymentTx{tx: tx, outboxTopics: s.outboxTopics, meterCommandTTL: s.meterCommandTTL}}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit meter operations transaction: %w", err)
	}
	return nil
}

// LockReconcileTarget locks the entitlement before the command, so every
// decision on one credit serializes in the same order.
func (t meterOperationsTx) LockReconcileTarget(ctx context.Context, commandID string) (domain.ReconcileTarget, error) {
	var target domain.ReconcileTarget
	err := t.tx.QueryRow(ctx, `SELECT mc.credit_id::text,sm.site_id FROM meter_commands mc JOIN smart_meters sm USING(meter_id) WHERE command_id=$1::uuid`, commandID).Scan(&target.CreditID, &target.SiteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, domain.ErrNotFound
	}
	if err != nil {
		return target, fmt.Errorf("read reconciliation command: %w", err)
	}
	if err := lockSiteCommandDelivery(ctx, t.tx, target.SiteID); err != nil {
		return target, err
	}
	if target.CreditID != nil {
		if _, err = t.tx.Exec(ctx, `SELECT credit_id FROM energy_credits WHERE credit_id=$1::uuid FOR UPDATE`, *target.CreditID); err != nil {
			return target, fmt.Errorf("lock energy credit: %w", err)
		}
	}
	target.Command, err = scanMeterCommand(t.tx.QueryRow(ctx, `SELECT `+meterCommandColumns+` FROM meter_commands mc WHERE command_id=$1::uuid FOR UPDATE`, commandID))
	if err != nil {
		return target, fmt.Errorf("lock reconciliation command: %w", err)
	}
	if err = t.tx.QueryRow(ctx, `SELECT site_id FROM smart_meters WHERE meter_id=$1`, target.Command.MeterID).Scan(&target.SiteID); err != nil {
		return target, fmt.Errorf("read command site: %w", err)
	}
	return target, nil
}

func (t meterOperationsTx) LoadCase(ctx context.Context, commandID string) (*domain.ReconciliationCase, error) {
	c, err := scanCase(t.tx.QueryRow(ctx, `SELECT `+caseColumns+` FROM command_reconciliation_cases WHERE command_id=$1::uuid`, commandID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read reconciliation case: %w", err)
	}
	return &c, nil
}

func (t meterOperationsTx) LatestConflict(ctx context.Context, commandID string) (*domain.RetainedCommandReport, error) {
	return readLatestCommandConflict(ctx, t.tx, commandID)
}

func (t meterOperationsTx) ClearCaseReview(ctx context.Context, commandID string) error {
	if _, err := t.tx.Exec(ctx, `UPDATE command_reconciliation_cases SET needs_review=false WHERE command_id=$1::uuid`, commandID); err != nil {
		return fmt.Errorf("clear case review: %w", err)
	}
	return nil
}

func (t meterOperationsTx) InvestigatedByAnother(ctx context.Context, commandID, actor string) (bool, error) {
	var found bool
	err := t.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE subject_type='meter_command' AND subject_id=$1
        AND action='meter_command.reconciliation.investigate' AND actor_id<>$2)`, commandID, actor).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("read case investigators: %w", err)
	}
	return found, nil
}

func (t meterOperationsTx) HasRetainedEvidence(ctx context.Context, commandID string) (bool, error) {
	var found bool
	err := t.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events a WHERE a.subject_id=$1 AND a.subject_type='meter_command' AND (a.action='meter_command.report_conflict' OR (`+legacyExpiryPredicate+`)))`, commandID).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("read retained command evidence: %w", err)
	}
	return found, nil
}

func (t meterOperationsTx) CreditSettlement(ctx context.Context, commandID, creditID string) (domain.CreditSettlement, error) {
	var s domain.CreditSettlement
	err := t.tx.QueryRow(ctx, `SELECT
        EXISTS(SELECT 1 FROM meter_commands WHERE replaces_command_id=$1::uuid),
        EXISTS(SELECT 1 FROM meter_credit_refunds WHERE credit_id=$2::uuid),
        EXISTS(SELECT 1 FROM meter_commands m WHERE m.credit_id=$2::uuid AND m.status='acknowledged'),
        EXISTS(SELECT 1 FROM meter_commands m JOIN audit_events a ON a.subject_id=m.command_id::text
            WHERE m.credit_id=$2::uuid AND a.subject_type='meter_command' AND a.action='meter_command.report_conflict'
            AND a.metadata->>'reported_status'='acknowledged'),
        EXISTS(SELECT 1 FROM meter_commands m JOIN audit_events a ON a.subject_id=m.command_id::text
            WHERE m.credit_id=$2::uuid AND `+uncertainDeliveryAuditPredicate+`),
        EXISTS(SELECT 1 FROM meter_commands m JOIN command_reconciliation_cases c USING(command_id)
            WHERE m.credit_id=$2::uuid AND c.disposition='delivered')`, commandID, creditID).
		Scan(&s.Superseded, &s.Refunded, &s.Acknowledged, &s.AcknowledgementConflict, &s.UncertainDelivery, &s.Closed)
	if err != nil {
		return s, fmt.Errorf("read credit settlement: %w", err)
	}
	return s, nil
}

func (t meterOperationsTx) RedeliveryAllowed(ctx context.Context, meterID, creditID string) (bool, error) {
	var allowed bool
	err := t.tx.QueryRow(ctx, `SELECT sm.credit_mode='meter' AND sm.status<>'retired' AND coalesce(p.prepaid_credit,false)
        AND EXISTS(SELECT 1 FROM energy_credits ec JOIN meter_assignments ma USING(assignment_id)
            WHERE ec.credit_id=$2::uuid AND ec.delivery_mode='meter' AND ma.meter_id=sm.meter_id AND ma.ended_at IS NULL)
        FROM smart_meters sm LEFT JOIN meter_command_profiles p USING(meter_id) WHERE sm.meter_id=$1 FOR SHARE OF sm`, meterID, creditID).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("check redelivery eligibility: %w", err)
	}
	return allowed, nil
}

func (t meterOperationsTx) InsertRedelivery(ctx context.Context, target domain.ReconcileTarget, actor, reason string) (string, error) {
	prior := target.Command
	cmd, err := scanMeterCommand(t.tx.QueryRow(ctx, `INSERT INTO meter_commands AS mc(meter_id,command_type,status,requested_by,reason,expires_at,credit_kwh,credit_id,replaces_command_id)
        VALUES($1,'credit_meter','requested',$2,$3,now()+make_interval(secs=>$4),$5,$6::uuid,$7::uuid) RETURNING `+meterCommandColumns, prior.MeterID, actor, reason, t.payment.meterCommandTTL.Seconds(), prior.CreditKWh, *target.CreditID, prior.CommandID))
	if err != nil {
		return "", fmt.Errorf("insert redelivery command: %w", err)
	}
	var assignmentID string
	if err = t.tx.QueryRow(ctx, `SELECT assignment_id::text FROM energy_credits WHERE credit_id=$1::uuid`, *target.CreditID).Scan(&assignmentID); err != nil {
		return "", fmt.Errorf("read credit assignment: %w", err)
	}
	if err = t.payment.InsertMeterCommandEvent(ctx, cmd, target.SiteID, assignmentID); err != nil {
		return "", err
	}
	return cmd.CommandID, nil
}

// RefundableCredit returns nil for anything but a paid meter credit. A missing
// balance row reads as an empty balance.
func (t meterOperationsTx) RefundableCredit(ctx context.Context, creditID string) (*domain.RefundableCredit, error) {
	var r domain.RefundableCredit
	var assignmentID string
	err := t.tx.QueryRow(ctx, `SELECT ec.assignment_id::text,ec.kwh_granted::text,ec.money_value_minor_units FROM energy_credits ec
        WHERE ec.credit_id=$1::uuid AND ec.payment_id IS NOT NULL AND ec.delivery_mode='meter' AND ec.money_value_minor_units>0`, creditID).Scan(&assignmentID, &r.Credit.KWh, &r.Credit.MoneyMinor)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read refundable credit: %w", err)
	}
	r.Balance = domain.CreditValue{KWh: "0"}
	err = t.tx.QueryRow(ctx, `SELECT remaining_kwh::text,remaining_money_value_minor_units FROM credit_balances WHERE assignment_id=$1::uuid FOR UPDATE`, assignmentID).Scan(&r.Balance.KWh, &r.Balance.MoneyMinor)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("lock credit balance: %w", err)
	}
	return &r, nil
}

func (t meterOperationsTx) RecordRefund(ctx context.Context, target domain.ReconcileTarget, r domain.ReconcileCommand, plan domain.CreditReversalPlan) error {
	if _, err := t.tx.Exec(ctx, `UPDATE credit_balances b SET remaining_kwh=b.remaining_kwh-$2::numeric,
        remaining_money_value_minor_units=b.remaining_money_value_minor_units-$3
        FROM energy_credits ec WHERE ec.credit_id=$1::uuid AND b.assignment_id=ec.assignment_id`, *target.CreditID, string(plan.Reversed.KWh), plan.Reversed.MoneyMinor); err != nil {
		return fmt.Errorf("reverse refunded credit: %w", err)
	}
	_, err := t.tx.Exec(ctx, `INSERT INTO meter_credit_refunds(credit_id,command_id,reference,actor,evidence,kwh,money_minor,shortfall_kwh,shortfall_money_minor)
        VALUES($1::uuid,$2::uuid,$3,$4,$5,$6::numeric,$7,$8::numeric,$9)`, *target.CreditID, target.Command.CommandID, r.RefundReference, r.Actor, r.Evidence,
		string(plan.Reversed.KWh), plan.Reversed.MoneyMinor, string(plan.Shortfall.KWh), plan.Shortfall.MoneyMinor)
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return fmt.Errorf("%w: refund reference already recorded", domain.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("record credit refund: %w", err)
	}
	return nil
}

func (t meterOperationsTx) SaveCase(ctx context.Context, c domain.ReconciliationCase) (domain.ReconciliationCase, error) {
	saved, err := scanCase(t.tx.QueryRow(ctx, `INSERT INTO command_reconciliation_cases(command_id,state,owner,finding,disposition,reason,evidence,resolved_by,replacement_command_id,refund_reference)
        VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9::uuid,$10)
        ON CONFLICT(command_id) DO UPDATE SET state=excluded.state,owner=excluded.owner,finding=excluded.finding,
        disposition=excluded.disposition,reason=excluded.reason,evidence=excluded.evidence,resolved_by=excluded.resolved_by,
        replacement_command_id=excluded.replacement_command_id,refund_reference=excluded.refund_reference,updated_at=now()
        RETURNING `+caseColumns, c.CommandID, c.State, c.Owner, c.Finding, c.Disposition, c.Reason, c.Evidence, c.ResolvedBy, c.ReplacementCommandID, c.RefundReference))
	if err != nil {
		return saved, fmt.Errorf("save reconciliation case: %w", err)
	}
	return saved, nil
}

func (t meterOperationsTx) InsertAuditEvent(ctx context.Context, siteID, actorID, action, subjectType, subjectID string, metadata map[string]any) error {
	return edgeCommandTx{t.tx}.InsertAuditEvent(ctx, siteID, actorID, action, subjectType, subjectID, metadata)
}

// LockCommandRequest makes concurrent retries of one request UUID take turns.
func (t meterOperationsTx) LockCommandRequest(ctx context.Context, requestID string) error {
	if _, err := t.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, requestID); err != nil {
		return fmt.Errorf("lock command request: %w", err)
	}
	return nil
}

func (t meterOperationsTx) FindCommand(ctx context.Context, commandID string) (*domain.MeterCommand, error) {
	cmd, err := scanMeterCommand(t.tx.QueryRow(ctx, `SELECT `+meterCommandColumns+` FROM meter_commands mc WHERE command_id=$1::uuid`, commandID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read meter command: %w", err)
	}
	return &cmd, nil
}

func (t meterOperationsTx) CommandProfile(ctx context.Context, meterID string) (domain.MeterCommandProfile, error) {
	var p domain.MeterCommandProfile
	err := t.tx.QueryRow(ctx, `SELECT sm.site_id,coalesce(p.relay_control,false),coalesce(p.status_read,false),coalesce(ma.assignment_id::text,''),
        sm.credit_mode='platform' AND ma.assignment_id IS NOT NULL AND coalesce(cb.remaining_kwh,0)<=0
        FROM smart_meters sm LEFT JOIN meter_command_profiles p USING(meter_id)
        LEFT JOIN meter_assignments ma ON ma.meter_id=sm.meter_id AND ma.ended_at IS NULL
        LEFT JOIN credit_balances cb ON cb.assignment_id=ma.assignment_id
        WHERE sm.meter_id=$1 AND sm.status<>'retired' FOR SHARE OF sm`, meterID).Scan(&p.SiteID, &p.RelayControl, &p.StatusRead, &p.AssignmentID, &p.BalanceExhausted)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, domain.ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("read meter command profile: %w", err)
	}
	return p, nil
}

func (t meterOperationsTx) InsertOperatorCommand(ctx context.Context, r domain.CreateMeterCommand, profile domain.MeterCommandProfile) (domain.MeterCommand, error) {
	cmd, err := scanMeterCommand(t.tx.QueryRow(ctx, `INSERT INTO meter_commands AS mc(command_id,meter_id,command_type,status,requested_by,reason,expires_at)
        VALUES($1::uuid,$2,$3,'requested',$4,$5,now()+make_interval(secs=>$6)) RETURNING `+meterCommandColumns, r.RequestID, r.MeterID, r.Type, r.Actor, r.Reason, t.payment.meterCommandTTL.Seconds()))
	if err != nil {
		return cmd, fmt.Errorf("insert operator command: %w", err)
	}
	if err = t.payment.InsertMeterCommandEvent(ctx, cmd, profile.SiteID, profile.AssignmentID); err != nil {
		return cmd, err
	}
	return cmd, nil
}

func (s *Store) GetMeterCommand(ctx context.Context, id string) (domain.MeterCommandDetail, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	var d domain.MeterCommandDetail
	cmd, err := scanMeterCommand(s.pool.QueryRow(ctx, `SELECT `+meterCommandColumns+` FROM meter_commands mc WHERE command_id=$1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, domain.ErrNotFound
	}
	if err != nil {
		return d, fmt.Errorf("read meter command: %w", err)
	}
	d.LatestConflict, err = readLatestCommandConflict(ctx, s.pool, id)
	if err != nil {
		return d, err
	}
	d.Command = cmd
	c, err := scanCase(s.pool.QueryRow(ctx, `SELECT `+caseColumns+` FROM command_reconciliation_cases WHERE command_id=$1::uuid`, id))
	if err == nil {
		d.Case = &c
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("read reconciliation case: %w", err)
	}
	d.Observation, d.ObservedAt, d.ReceivedAt, err = readCommandObservation(ctx, s.pool, id)
	if err != nil {
		return d, err
	}
	return d, nil
}

func (s *Store) ListResolvedCases(ctx context.Context, limit int, after string) ([]domain.ReconciliationCase, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT `+caseColumns+` FROM command_reconciliation_cases WHERE state='resolved' AND ($1='' OR command_id>NULLIF($1,'')::uuid) ORDER BY command_id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list resolved cases: %w", err)
	}
	defer rows.Close()
	result := make([]domain.ReconciliationCase, 0)
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, fmt.Errorf("read resolved case: %w", err)
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read resolved cases: %w", err)
	}
	return result, nil
}

func (s *Store) ListRefundShortfalls(ctx context.Context, limit int, after string) ([]domain.RefundShortfall, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT credit_id::text,command_id::text,reference,shortfall_kwh::text,shortfall_money_minor,refunded_at
        FROM meter_credit_refunds WHERE (shortfall_kwh>0 OR shortfall_money_minor>0) AND ($1='' OR credit_id>NULLIF($1,'')::uuid)
        ORDER BY credit_id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list refund shortfalls: %w", err)
	}
	shortfalls, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.RefundShortfall, error) {
		var r domain.RefundShortfall
		err := row.Scan(&r.CreditID, &r.CommandID, &r.Reference, &r.Shortfall.KWh, &r.Shortfall.MoneyMinor, &r.RefundedAt)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("read refund shortfalls: %w", err)
	}
	return shortfalls, nil
}
