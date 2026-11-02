package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"time"

	"api-go/internal/domain"
	"api-go/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

type STSVendStore struct {
	pool         *pgxpool.Pool
	installation string
}

// Installation binds references to one vending backend; changing endpoint must
// not silently reuse a reservation made against another provider installation.
func NewSTSVendStore(pool *pgxpool.Pool, installation string) *STSVendStore {
	return &STSVendStore{pool: pool, installation: installation}
}
func (s *stsVendSession) Reserve(ctx context.Context, request domain.STSVendRequest) (*domain.STSVend, bool, error) {
	if request.Reference != s.reference {
		return nil, false, errors.New("reservation differs from owned reference")
	}
	if err := request.ValidateFunding(); err != nil {
		return nil, false, fmt.Errorf("validate STS vend reservation funding: %w", err)
	}
	prior, err := s.existing(ctx, request)
	if err != nil {
		return nil, false, err
	}
	if prior.found {
		if prior.retryable {
			_, err := s.conn.Exec(ctx, `UPDATE sts_vends SET safe_to_retry=false,last_error=NULL WHERE reference=$1`, request.Reference)
			if err != nil {
				return nil, false, fmt.Errorf("reset STS vend for retry: %w", err)
			}
			return nil, true, nil
		}
		return prior.saved, false, nil
	}
	tag, err := s.conn.Exec(ctx, `INSERT INTO sts_vends(reference,installation,meter_id,amount_minor,payment_id,requested_by) VALUES($1,$2,$3,$4,$5::uuid,$6) ON CONFLICT(reference) DO NOTHING`, request.Reference, s.installation, request.MeterID, request.AmountMinor, request.PaymentID, request.RequestedBy)
	if err != nil {
		return nil, false, fmt.Errorf("insert STS vend reservation: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil, true, nil
	}
	prior, err = s.existing(ctx, request)
	return prior.saved, false, err
}

type stsVendReservation struct {
	saved            *domain.STSVend
	found, retryable bool
}

func (s *stsVendSession) existing(ctx context.Context, request domain.STSVendRequest) (stsVendReservation, error) {
	var installation, meter string
	var payment *string
	var amount int64
	var result []byte
	var notIssued *time.Time
	var retryable bool
	err := s.conn.QueryRow(ctx, `SELECT installation,meter_id,amount_minor,result,payment_id::text,not_issued_at,safe_to_retry FROM sts_vends WHERE reference=$1`, request.Reference).Scan(&installation, &meter, &amount, &result, &payment, &notIssued, &retryable)
	if errors.Is(err, pgx.ErrNoRows) {
		return stsVendReservation{}, nil
	}
	if err != nil {
		return stsVendReservation{}, fmt.Errorf("read STS vend reservation: %w", err)
	}
	if installation != s.installation || meter != request.MeterID || amount != request.AmountMinor || payment == nil || *payment != request.PaymentID {
		return stsVendReservation{found: true}, fmt.Errorf("%w: vending reference used with different parameters", domain.ErrConflict)
	}
	if notIssued != nil {
		return stsVendReservation{found: true}, domain.ErrVendNotIssued
	}
	if result == nil {
		return stsVendReservation{found: true, retryable: retryable}, nil
	}
	var saved domain.STSVend
	if err := json.Unmarshal(result, &saved); err != nil {
		return stsVendReservation{found: true}, fmt.Errorf("decode stored STS vend result: %w", err)
	}
	return stsVendReservation{saved: &saved, found: true}, nil
}
func (s *stsVendSession) Complete(ctx context.Context, reference string, result domain.STSVend) error {
	if reference != s.reference {
		return fmt.Errorf("completion differs from owned reference")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode STS vend result: %w", err)
	}
	tag, err := s.conn.Exec(ctx, `UPDATE sts_vends SET result=$2::jsonb,completed_at=now(),safe_to_retry=false,last_error=NULL WHERE reference=$1 AND installation=$3 AND result IS NULL AND not_issued_at IS NULL`, reference, string(raw), s.installation)
	if err != nil {
		return fmt.Errorf("complete STS vend reservation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("vending reservation missing or already completed")
	}
	return nil
}

// stsListLimit bounds an operator listing to one screen of work.
const stsListLimit = 200

func (s *STSVendStore) ListReversalShortfalls(ctx context.Context) ([]domain.STSReversalShortfall, error) {
	rows, err := s.pool.Query(ctx, `SELECT reference,credit_id,shortfall_kwh,shortfall_money_minor,at FROM (
  SELECT scr.reference,scr.credit_id::text AS credit_id,scr.shortfall_kwh::text AS shortfall_kwh,scr.shortfall_money_minor,scr.reversed_at AS at
  FROM sts_credit_reversals scr JOIN sts_vends sv USING(reference)
  WHERE sv.installation=$1 AND (scr.shortfall_kwh>0 OR scr.shortfall_money_minor>0)
  UNION ALL
  SELECT r.reference,r.credit_id::text,r.shortfall_kwh::text,r.shortfall_money_minor,r.refunded_at
  FROM sts_credit_refunds r JOIN energy_credits ec USING(credit_id)
  JOIN meter_assignments ma USING(assignment_id) JOIN smart_meters sm ON sm.meter_id=ma.meter_id
  WHERE sm.sts_installation=$1 AND (r.shortfall_kwh>0 OR r.shortfall_money_minor>0)
 ) shortfalls ORDER BY at,reference LIMIT $2`, s.installation, stsListLimit)
	if err != nil {
		return nil, fmt.Errorf("list STS reversal shortfalls: %w", err)
	}
	shortfalls, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.STSReversalShortfall, error) {
		var r domain.STSReversalShortfall
		err := row.Scan(&r.Reference, &r.CreditID, &r.ShortfallKWh, &r.ShortfallMoneyMinor, &r.ReversedAt)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan STS reversal shortfalls: %w", err)
	}
	return shortfalls, nil
}

// Any installation's reservation counts: the credit is then tracked by that vend.
// Only confirmed payments are work; a reversed payment has nothing left to vend.
func (s *STSVendStore) ListUnvendedCredits(ctx context.Context, after string) ([]domain.STSUnvendedCredit, error) {
	rows, err := s.pool.Query(ctx, `SELECT ec.payment_id::text,ec.credit_id::text,ec.site_id,ec.kwh_granted::text,COALESCE(ec.money_value_minor_units,0),ec.created_at,
 sts_funding_block_reason(ec.payment_id::text,ec.payment_id,sm.sts_meter_number,p.amount_minor_units,$3)
 FROM energy_credits ec JOIN payments p USING(payment_id)
 JOIN meter_assignments ma USING(assignment_id) JOIN smart_meters sm ON sm.meter_id=ma.meter_id
 WHERE ec.delivery_mode='sts' AND p.status='confirmed'
 AND NOT EXISTS(SELECT 1 FROM sts_vends sv WHERE sv.payment_id=ec.payment_id)
 AND NOT EXISTS(SELECT 1 FROM sts_credit_refunds r WHERE r.credit_id=ec.credit_id)
 AND ($2='' OR (ec.created_at,ec.credit_id)>(SELECT created_at,credit_id FROM energy_credits WHERE credit_id=NULLIF($2,'')::uuid))
 ORDER BY ec.created_at,ec.credit_id LIMIT $1`, stsListLimit, after, s.installation)
	if err != nil {
		return nil, fmt.Errorf("list unvended STS credits: %w", err)
	}
	credits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.STSUnvendedCredit, error) {
		var c domain.STSUnvendedCredit
		err := row.Scan(&c.PaymentID, &c.CreditID, &c.SiteID, &c.KWhGranted, &c.AmountMinor, &c.CreditedAt, &c.BlockedReason)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan unvended STS credits: %w", err)
	}
	return credits, nil
}

func (s *STSVendStore) ListUnresolved(ctx context.Context) ([]domain.STSVendRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT reference,payment_id::text,meter_id,amount_minor,requested_at,
 not_issued_at IS NOT NULL,safe_to_retry,last_error,
 CASE WHEN safe_to_retry THEN sts_funding_block_reason(reference,payment_id,meter_id,amount_minor,installation) END
 FROM sts_vends sv WHERE installation=$1 AND result IS NULL AND (
  not_issued_at IS NULL OR
  (not_issued_at IS NOT NULL AND payment_id IS NOT NULL AND NOT EXISTS(
   SELECT 1 FROM sts_credit_reversals WHERE reference=sv.reference)))
 ORDER BY requested_at,reference LIMIT $2`, s.installation, stsListLimit)
	if err != nil {
		return nil, fmt.Errorf("list unresolved STS vends: %w", err)
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.STSVendRecord, error) {
		var r domain.STSVendRecord
		err := row.Scan(&r.Reference, &r.PaymentID, &r.MeterID, &r.AmountMinor, &r.RequestedAt,
			&r.NeedsBalanceReversal, &r.SafeToRetry, &r.LastError, &r.RetryBlockedReason)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan unresolved STS vends: %w", err)
	}
	return records, nil
}

func (s *STSVendStore) Reconcile(ctx context.Context, r domain.STSReconciliation) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("validate STS reconciliation: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin STS reconciliation: %w", err)
	}
	defer tx.Rollback(ctx)
	var owned bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('sts-vend:'||$1,0))`, r.Reference).Scan(&owned); err != nil {
		return fmt.Errorf("lock STS vend for reconciliation: %w", err)
	}
	if !owned {
		return fmt.Errorf("%w: vend is still active", domain.ErrConflict)
	}

	var meter string
	var existing []byte
	var notIssued *time.Time
	var paymentID *string
	if err := tx.QueryRow(ctx, `SELECT meter_id,result,not_issued_at,payment_id::text FROM sts_vends WHERE reference=$1 AND installation=$2 FOR UPDATE`, r.Reference, s.installation).Scan(&meter, &existing, &notIssued, &paymentID); err != nil {
		return fmt.Errorf("read STS vend for reconciliation: %w", err)
	}
	state := domain.STSReservationState{MeterID: meter, Completed: existing != nil, NotIssued: notIssued != nil, Funded: paymentID != nil}
	if state.NotIssued {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sts_credit_reversals WHERE reference=$1)`, r.Reference).Scan(&state.CreditReversed); err != nil {
			return fmt.Errorf("check STS credit reversal: %w", err)
		}
	}
	plan, err := state.Reconcile(r)
	if err != nil {
		return err
	}
	if plan.Outcome == domain.STSOutcomeRecovered {
		raw, err := json.Marshal(r.Result)
		if err != nil {
			return fmt.Errorf("encode recovered STS vend result: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE sts_vends SET result=$2::jsonb,completed_at=now(),safe_to_retry=false,last_error=NULL WHERE reference=$1`, r.Reference, string(raw)); err != nil {
			return fmt.Errorf("save recovered STS vend result: %w", err)
		}
	} else {
		if plan.ReverseCredit {
			if err := reverseSTSCredit(ctx, tx, r); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sts_vends SET not_issued_at=COALESCE(not_issued_at,now()),safe_to_retry=false WHERE reference=$1`, r.Reference); err != nil {
			return fmt.Errorf("record STS vend non-issuance: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sts_vend_reconciliations(reference,actor,evidence,outcome) VALUES($1,$2,$3,$4)`, r.Reference, r.Actor, r.Evidence, plan.Outcome); err != nil {
		return fmt.Errorf("record STS reconciliation audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit STS reconciliation: %w", err)
	}
	return nil
}

// RefundUnvendedCredit shares the vend's exclusive lock and refuses any existing
// reservation, so a token and a refund can never both be issued for one payment.
func (s *STSVendStore) RefundUnvendedCredit(ctx context.Context, r domain.STSCreditRefund) (domain.CreditReversalPlan, error) {
	var plan domain.CreditReversalPlan
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return plan, fmt.Errorf("begin STS refund: %w", err)
	}
	defer tx.Rollback(ctx)
	var owned bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('sts-vend:'||$1,0))`, r.PaymentID).Scan(&owned); err != nil {
		return plan, fmt.Errorf("lock STS vend for refund: %w", err)
	}
	if !owned {
		return plan, fmt.Errorf("%w: vend is still active", domain.ErrConflict)
	}
	var creditID, assignmentID, siteID, status string
	var credit domain.CreditValue
	err = tx.QueryRow(ctx, `SELECT ec.credit_id::text,ec.assignment_id::text,ec.site_id,p.status,ec.kwh_granted::text,COALESCE(ec.money_value_minor_units,0)
 FROM energy_credits ec JOIN payments p USING(payment_id)
 WHERE ec.payment_id=$1::uuid AND ec.delivery_mode='sts' FOR UPDATE OF ec`, r.PaymentID).Scan(&creditID, &assignmentID, &siteID, &status, &credit.KWh, &credit.MoneyMinor)
	if errors.Is(err, pgx.ErrNoRows) {
		return plan, fmt.Errorf("%w: no STS credit for payment %s", domain.ErrNotFound, r.PaymentID)
	}
	if err != nil {
		return plan, fmt.Errorf("read STS credit for refund: %w", err)
	}
	if status != string(domain.PaymentStatusConfirmed) {
		return plan, fmt.Errorf("%w: payment is %s", domain.ErrConflict, status)
	}
	var vended, refunded bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sts_vends WHERE payment_id=$1::uuid),EXISTS(SELECT 1 FROM sts_credit_refunds WHERE payment_id=$1::uuid)`, r.PaymentID).Scan(&vended, &refunded); err != nil {
		return plan, fmt.Errorf("check STS refund eligibility: %w", err)
	}
	if vended {
		return plan, fmt.Errorf("%w: a vend exists for this payment; reconcile it instead", domain.ErrConflict)
	}
	if refunded {
		return plan, fmt.Errorf("%w: payment was already refunded", domain.ErrConflict)
	}
	balance := domain.CreditValue{KWh: "0"}
	err = tx.QueryRow(ctx, `SELECT remaining_kwh::text,remaining_money_value_minor_units FROM credit_balances WHERE assignment_id=$1::uuid FOR UPDATE`, assignmentID).Scan(&balance.KWh, &balance.MoneyMinor)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return plan, fmt.Errorf("lock STS credit balance: %w", err)
	}
	if plan, err = domain.PlanCreditReversal(credit, balance); err != nil {
		return plan, err
	}
	if _, err := tx.Exec(ctx, `UPDATE credit_balances SET remaining_kwh=remaining_kwh-$2::numeric,remaining_money_value_minor_units=remaining_money_value_minor_units-$3 WHERE assignment_id=$1::uuid`, assignmentID, string(plan.Reversed.KWh), plan.Reversed.MoneyMinor); err != nil {
		return plan, fmt.Errorf("reverse refunded STS credit: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO sts_credit_refunds(credit_id,payment_id,reference,actor,evidence,kwh,money_minor,shortfall_kwh,shortfall_money_minor)
 VALUES($1::uuid,$2::uuid,$3,$4,$5,$6::numeric,$7,$8::numeric,$9)`, creditID, r.PaymentID, r.Reference, r.Actor, r.Evidence, string(plan.Reversed.KWh), plan.Reversed.MoneyMinor, string(plan.Shortfall.KWh), plan.Shortfall.MoneyMinor)
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return plan, fmt.Errorf("%w: refund reference already recorded", domain.ErrConflict)
	}
	if err != nil {
		return plan, fmt.Errorf("record STS refund: %w", err)
	}
	metadata := map[string]any{"payment_id": r.PaymentID, "reference": r.Reference, "evidence": r.Evidence, "reversed": plan.Reversed, "shortfall": plan.Shortfall}
	if err := (edgeCommandTx{tx}).InsertAuditEvent(ctx, siteID, r.Actor, "energy_credit.sts_refunded", "energy_credit", creditID, metadata); err != nil {
		return plan, err
	}
	if err := tx.Commit(ctx); err != nil {
		return plan, fmt.Errorf("commit STS refund: %w", err)
	}
	return plan, nil
}

type stsVendSession struct {
	conn                    *pgxpool.Conn
	installation, reference string
}

// Open keeps a session lock across SQL and the external exchange on one
// connection. Other references cannot exhaust the pool while awaiting their own SQL.
func (s *STSVendStore) Open(ctx context.Context, reference string) (services.STSVendSession, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire STS vend connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('sts-vend:'||$1,0))`, reference).Scan(&acquired); err != nil {
		// The server may have acquired the lock before the reply was lost.
		// Discard this session rather than returning an unknown lock to the pool.
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		conn.Conn().Close(closeCtx)
		cancel()
		conn.Release()
		return nil, fmt.Errorf("lock STS vend session: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, domain.ErrVendInProgress
	}
	return &stsVendSession{conn: conn, installation: s.installation, reference: reference}, nil
}
func (s *stsVendSession) Close() {
	unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var unlocked bool
	if err := s.conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended('sts-vend:'||$1,0))`, s.reference).Scan(&unlocked); err != nil || !unlocked {
		s.conn.Conn().Close(unlockCtx)
	}
	s.conn.Release()
}

func (s *stsVendSession) RecordUnsent(ctx context.Context, reference, cause string) error {
	if reference != s.reference {
		return fmt.Errorf("unsent result differs from owned reference")
	}
	tag, err := s.conn.Exec(ctx, `UPDATE sts_vends SET safe_to_retry=true,last_error=$2 WHERE reference=$1 AND installation=$3 AND result IS NULL AND not_issued_at IS NULL`, reference, cause, s.installation)
	if err != nil {
		return fmt.Errorf("record unsent STS vend: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("unsent reservation missing or resolved")
	}
	return nil
}

type stsCreditReversal struct {
	creditID, assignmentID string
	credit                 domain.CreditValue
}

// reverseSTSCredit undoes a jointly affordable portion of the original credit.
// Unreversed amounts, including rounding remainders, are recorded as shortfalls.
func reverseSTSCredit(ctx context.Context, tx pgx.Tx, r domain.STSReconciliation) error {
	rows, err := tx.Query(ctx, `SELECT ec.credit_id::text,ec.assignment_id::text,ec.kwh_granted::text,COALESCE(ec.money_value_minor_units,0)
    FROM sts_vends sv JOIN energy_credits ec USING(payment_id)
    WHERE sv.reference=$1 AND ec.delivery_mode='sts'`, r.Reference)
	if err != nil {
		return fmt.Errorf("read STS credit for reversal: %w", err)
	}
	var reversals []stsCreditReversal
	for rows.Next() {
		var v stsCreditReversal
		if err := rows.Scan(&v.creditID, &v.assignmentID, &v.credit.KWh, &v.credit.MoneyMinor); err != nil {
			rows.Close()
			return fmt.Errorf("scan STS credit for reversal: %w", err)
		}
		reversals = append(reversals, v)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read STS credit for reversal: %w", err)
	}
	if len(reversals) != 1 {
		return fmt.Errorf("%w: payment for %s has %d STS credits, want 1", domain.ErrConflict, r.Reference, len(reversals))
	}

	v := reversals[0]
	balance := domain.CreditValue{KWh: "0", MoneyMinor: 0}
	err = tx.QueryRow(ctx, `SELECT remaining_kwh::text,remaining_money_value_minor_units FROM credit_balances WHERE assignment_id=$1::uuid FOR UPDATE`, v.assignmentID).Scan(&balance.KWh, &balance.MoneyMinor)
	hasBalance := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock STS credit balance: %w", err)
	}
	plan, err := domain.PlanCreditReversal(v.credit, balance)
	if err != nil {
		return err
	}
	if hasBalance {
		if _, err := tx.Exec(ctx, `UPDATE credit_balances SET remaining_kwh=remaining_kwh-$2::numeric,remaining_money_value_minor_units=remaining_money_value_minor_units-$3 WHERE assignment_id=$1::uuid`, v.assignmentID, string(plan.Reversed.KWh), plan.Reversed.MoneyMinor); err != nil {
			return fmt.Errorf("reverse STS credit balance: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sts_credit_reversals(reference,credit_id,kwh,money_minor,shortfall_kwh,shortfall_money_minor,actor,evidence) VALUES($1,$2::uuid,$3::numeric,$4,$5::numeric,$6,$7,$8)`, r.Reference, v.creditID, string(plan.Reversed.KWh), plan.Reversed.MoneyMinor, string(plan.Shortfall.KWh), plan.Shortfall.MoneyMinor, r.Actor, r.Evidence); err != nil {
		return fmt.Errorf("record STS credit reversal: %w", err)
	}
	return nil
}
