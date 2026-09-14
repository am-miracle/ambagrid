// Defines shared Postgres repository helpers.
package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"api-go/internal/domain"
)

// Store reads the operational database with bounded query time.
type Store struct {
	pool         *pgxpool.Pool
	queryTimeout time.Duration
}

func NewStore(pool *pgxpool.Pool, queryTimeout time.Duration) *Store {
	return &Store{pool: pool, queryTimeout: queryTimeout}
}

// Ping checks database readiness.
func (s *Store) Ping(ctx context.Context) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.pool.Ping(ctx)
}

func (s *Store) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.queryTimeout)
}

type scanner interface {
	Scan(dest ...any) error
}

// parseEnum rejects a stored enum value that no domain Parse* function recognizes.
func parseEnum[T any](id string, parse func(string) (T, error), value string) (T, error) {
	parsed, err := parse(value)
	if err != nil {
		return parsed, fmt.Errorf("%w: %q: %s", domain.ErrInvalidData, id, err)
	}
	return parsed, nil
}

// predicates builds indexed WHERE clauses with bound parameters.
type predicates struct {
	clauses []string
	args    []any
}

func newPredicates() *predicates {
	return &predicates{}
}

func (p *predicates) bind(value any) string {
	p.args = append(p.args, value)
	return "$" + strconv.Itoa(len(p.args))
}

func (p *predicates) add(prefix string, value any) {
	p.clauses = append(p.clauses, prefix+p.bind(value))
}

func (p *predicates) addLiteral(clause string) {
	p.clauses = append(p.clauses, clause)
}

func (p *predicates) finish(selectSQL, orderBy string, limit int) (string, []any) {
	sql := selectSQL
	if len(p.clauses) > 0 {
		sql += "\nWHERE " + strings.Join(p.clauses, "\n  AND ")
	}
	sql += "\n" + orderBy + "\nLIMIT " + p.bind(limit)
	return sql, p.args
}
