package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
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
		return nil, false, fmt.Errorf("reservation differs from owned reference")
	}
	if err := request.ValidateFunding(); err != nil {
		return nil, false, err
	}
	saved, found, retryable, err := s.existing(ctx, request)
	if err != nil {
		return nil, false, err
	}
	if found {
		if retryable {
			_, err := s.conn.Exec(ctx, `UPDATE sts_vends SET safe_to_retry=false,last_error=NULL WHERE reference=$1`, request.Reference)
			return nil, err == nil, err
		}
		return saved, false, nil
	}
	tag, err := s.conn.Exec(ctx, `INSERT INTO sts_vends(reference,installation,meter_id,amount_minor,payment_id) VALUES($1,$2,$3,$4,$5::uuid) ON CONFLICT(reference) DO NOTHING`, request.Reference, s.installation, request.MeterID, request.AmountMinor, request.PaymentID)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 1 {
		return nil, true, nil
	}
	saved, _, _, err = s.existing(ctx, request)
	return saved, false, err
}
func (s *stsVendSession) existing(ctx context.Context, request domain.STSVendRequest) (*domain.STSVend, bool, bool, error) {
	var installation, meter string
	var payment *string
	var amount int64
	var result []byte
	var notIssued *time.Time
	var retryable bool
	err := s.conn.QueryRow(ctx, `SELECT installation,meter_id,amount_minor,result,payment_id::text,not_issued_at,safe_to_retry FROM sts_vends WHERE reference=$1`, request.Reference).Scan(&installation, &meter, &amount, &result, &payment, &notIssued, &retryable)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	if installation != s.installation || meter != request.MeterID || amount != request.AmountMinor || payment == nil || *payment != request.PaymentID {
		return nil, true, false, fmt.Errorf("%w: vending reference used with different parameters", domain.ErrConflict)
	}
	if notIssued != nil {
		return nil, true, false, domain.ErrVendNotIssued
	}
	if result == nil {
		return nil, true, retryable, nil
	}
	var saved domain.STSVend
	if err := json.Unmarshal(result, &saved); err != nil {
		return nil, true, false, err
	}
	return &saved, true, false, nil
}
func (s *stsVendSession) Complete(ctx context.Context, reference string, result domain.STSVend) error {
	if reference != s.reference {
		return fmt.Errorf("completion differs from owned reference")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tag, err := s.conn.Exec(ctx, `UPDATE sts_vends SET result=$2::jsonb,completed_at=now(),safe_to_retry=false,last_error=NULL WHERE reference=$1 AND installation=$3 AND result IS NULL AND not_issued_at IS NULL`, reference, string(raw), s.installation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("vending reservation missing or already completed")
	}
	return nil
}

func (s *STSVendStore) ListUnresolved(ctx context.Context) ([]domain.STSVendRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT reference,payment_id::text,meter_id,amount_minor,requested_at,not_issued_at IS NOT NULL
 FROM sts_vends sv WHERE installation=$1 AND result IS NULL AND (
  (not_issued_at IS NULL AND NOT safe_to_retry) OR
  (not_issued_at IS NOT NULL AND payment_id IS NOT NULL AND NOT EXISTS(
   SELECT 1 FROM sts_credit_reversals WHERE reference=sv.reference)))
 ORDER BY requested_at,reference LIMIT 200`, s.installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []domain.STSVendRecord{}
	for rows.Next() {
		var r domain.STSVendRecord
		if err := rows.Scan(&r.Reference, &r.PaymentID, &r.MeterID, &r.AmountMinor, &r.RequestedAt, &r.NeedsBalanceReversal); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}
func (s *STSVendStore) Reconcile(ctx context.Context, r domain.STSReconciliation) error {
	if err := r.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owned bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('sts-vend:'||$1,0))`, r.Reference).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("%w: vend is still active", domain.ErrConflict)
	}

	var meter string
	var existing []byte
	var notIssued *time.Time
	var paymentID *string
	if err := tx.QueryRow(ctx, `SELECT meter_id,result,not_issued_at,payment_id::text FROM sts_vends WHERE reference=$1 AND installation=$2 FOR UPDATE`, r.Reference, s.installation).Scan(&meter, &existing, &notIssued, &paymentID); err != nil {
		return err
	}
	if meter != r.MeterID || existing != nil {
		return fmt.Errorf("%w: reservation already resolved or meter differs", domain.ErrConflict)
	}
	if notIssued != nil {
		var reversed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sts_credit_reversals WHERE reference=$1)`, r.Reference).Scan(&reversed); err != nil {
			return err
		}
		if !r.NotIssued || paymentID == nil || reversed {
			return fmt.Errorf("%w: non-issuance already resolved", domain.ErrConflict)
		}
	}
	outcome := "not_issued"
	if r.Result != nil {
		outcome = "recovered"
		raw, err := json.Marshal(r.Result)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sts_vends SET result=$2::jsonb,completed_at=now(),safe_to_retry=false,last_error=NULL WHERE reference=$1`, r.Reference, string(raw)); err != nil {
			return err
		}
	} else {
		if paymentID != nil {
			// Exact SQL numeric arithmetic avoids float conversion and never clips an
			// insufficient balance. Failure rolls back the resolution and audit too.
			tag, err := tx.Exec(ctx, `UPDATE credit_balances cb
    SET remaining_kwh=cb.remaining_kwh-ec.kwh_granted,
        remaining_money_value_minor_units=cb.remaining_money_value_minor_units-COALESCE(ec.money_value_minor_units,0)
    FROM energy_credits ec JOIN sts_vends sv ON sv.payment_id=ec.payment_id
    WHERE sv.reference=$1 AND ec.delivery_mode='sts' AND cb.assignment_id=ec.assignment_id
      AND cb.remaining_kwh>=ec.kwh_granted AND cb.remaining_money_value_minor_units>=COALESCE(ec.money_value_minor_units,0)`, r.Reference)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("%w: insufficient or missing STS credit balance for reversal", domain.ErrConflict)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO sts_credit_reversals(reference,credit_id,kwh,money_minor,actor,evidence)
    SELECT sv.reference,ec.credit_id,ec.kwh_granted,COALESCE(ec.money_value_minor_units,0),$2,$3
    FROM sts_vends sv JOIN energy_credits ec USING(payment_id) WHERE sv.reference=$1`, r.Reference, r.Actor, r.Evidence); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sts_vends SET not_issued_at=COALESCE(not_issued_at,now()),safe_to_retry=false WHERE reference=$1`, r.Reference); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sts_vend_reconciliations(reference,actor,evidence,outcome) VALUES($1,$2,$3,$4)`, r.Reference, r.Actor, r.Evidence, outcome); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
		return nil, err
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('sts-vend:'||$1,0))`, reference).Scan(&acquired); err != nil {
		// The server may have acquired the lock before the reply was lost.
		// Discard this session rather than returning an unknown lock to the pool.
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		conn.Conn().Close(closeCtx)
		cancel()
		conn.Release()
		return nil, err
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
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("unsent reservation missing or resolved")
	}
	return nil
}
