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
	if saved, found, err := s.existing(ctx, request); found || err != nil {
		return saved, false, err
	}
	tag, err := s.conn.Exec(ctx, `INSERT INTO sts_vends(reference,installation,meter_id,amount_minor,payment_id) VALUES($1,$2,$3,$4,$5::uuid) ON CONFLICT(reference) DO NOTHING`, request.Reference, s.installation, request.MeterID, request.AmountMinor, request.PaymentID)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 1 {
		return nil, true, nil
	}
	saved, _, err := s.existing(ctx, request)
	return saved, false, err
}
func (s *stsVendSession) existing(ctx context.Context, request domain.STSVendRequest) (*domain.STSVend, bool, error) {
	var installation, meter string
	var payment *string
	var amount int64
	var result []byte
	var notIssued *time.Time
	err := s.conn.QueryRow(ctx, `SELECT installation,meter_id,amount_minor,result,payment_id::text,not_issued_at FROM sts_vends WHERE reference=$1`, request.Reference).Scan(&installation, &meter, &amount, &result, &payment, &notIssued)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if installation != s.installation || meter != request.MeterID || amount != request.AmountMinor || payment == nil || *payment != request.PaymentID {
		return nil, true, fmt.Errorf("%w: vending reference used with different parameters", domain.ErrConflict)
	}
	if notIssued != nil {
		return nil, true, domain.ErrVendNotIssued
	}
	if result == nil {
		return nil, true, nil
	}
	var saved domain.STSVend
	if err := json.Unmarshal(result, &saved); err != nil {
		return nil, true, err
	}
	return &saved, true, nil
}
func (s *stsVendSession) Complete(ctx context.Context, reference string, result domain.STSVend) error {
	if reference != s.reference {
		return fmt.Errorf("completion differs from owned reference")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tag, err := s.conn.Exec(ctx, `UPDATE sts_vends SET result=$2::jsonb,completed_at=now() WHERE reference=$1 AND installation=$3 AND result IS NULL AND not_issued_at IS NULL`, reference, string(raw), s.installation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("vending reservation missing or already completed")
	}
	return nil
}

func (s *STSVendStore) ListUnresolved(ctx context.Context) ([]domain.STSVendRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT reference,payment_id::text,meter_id,amount_minor,requested_at FROM sts_vends WHERE installation=$1 AND result IS NULL AND not_issued_at IS NULL ORDER BY requested_at,reference LIMIT 200`, s.installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []domain.STSVendRecord{}
	for rows.Next() {
		var r domain.STSVendRecord
		if err := rows.Scan(&r.Reference, &r.PaymentID, &r.MeterID, &r.AmountMinor, &r.RequestedAt); err != nil {
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
	if err := tx.QueryRow(ctx, `SELECT meter_id,result,not_issued_at FROM sts_vends WHERE reference=$1 AND installation=$2 FOR UPDATE`, r.Reference, s.installation).Scan(&meter, &existing, &notIssued); err != nil {
		return err
	}
	if meter != r.MeterID || existing != nil || notIssued != nil {
		return fmt.Errorf("%w: reservation already resolved or meter differs", domain.ErrConflict)
	}
	outcome := "not_issued"
	if r.Result != nil {
		outcome = "recovered"
		raw, err := json.Marshal(r.Result)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sts_vends SET result=$2::jsonb,completed_at=now() WHERE reference=$1`, r.Reference, string(raw)); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE sts_vends SET not_issued_at=now() WHERE reference=$1`, r.Reference); err != nil {
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
		return nil, domain.ErrVendUncertain
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
