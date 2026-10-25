package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"api-go/internal/domain"
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
func (s *STSVendStore) Reserve(ctx context.Context, request domain.STSVendRequest) (*domain.STSVend, bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO sts_vends(reference,installation,meter_id,amount_minor) VALUES($1,$2,$3,$4) ON CONFLICT(reference) DO NOTHING`, request.Reference, s.installation, request.MeterID, request.AmountMinor)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 1 {
		return nil, true, nil
	}
	var installation, meter string
	var amount int64
	var result []byte
	if err := s.pool.QueryRow(ctx, `SELECT installation,meter_id,amount_minor,result FROM sts_vends WHERE reference=$1`, request.Reference).Scan(&installation, &meter, &amount, &result); err != nil {
		return nil, false, err
	}
	if installation != s.installation || meter != request.MeterID || amount != request.AmountMinor {
		return nil, false, fmt.Errorf("%w: vending reference used with different parameters", domain.ErrConflict)
	}
	if result == nil {
		return nil, false, nil
	}
	var saved domain.STSVend
	if err := json.Unmarshal(result, &saved); err != nil {
		return nil, false, err
	}
	return &saved, false, nil
}
func (s *STSVendStore) Complete(ctx context.Context, reference string, result domain.STSVend) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE sts_vends SET result=$2::jsonb,completed_at=now() WHERE reference=$1 AND installation=$3 AND result IS NULL`, reference, string(raw), s.installation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("vending reservation missing or already completed")
	}
	return nil
}
