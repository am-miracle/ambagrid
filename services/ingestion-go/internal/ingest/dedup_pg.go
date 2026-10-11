package ingest

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresDedupStore struct {
	pool *pgxpool.Pool
}

func NewPostgresDedupStore(pool *pgxpool.Pool) *PostgresDedupStore {
	return &PostgresDedupStore{pool: pool}
}

func (s *PostgresDedupStore) TryInsert(ctx context.Context, siteID string, sequence uint64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		"INSERT INTO dedup_log (site_id, sequence) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		siteID, int64(sequence))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresDedupStore) Remove(ctx context.Context, siteID string, sequence uint64) error {
	_, err := s.pool.Exec(ctx,
		"DELETE FROM dedup_log WHERE site_id = $1 AND sequence = $2",
		siteID, int64(sequence))
	return err
}
