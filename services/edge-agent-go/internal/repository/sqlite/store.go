// Package sqlite persists the edge agent's durable store-and-forward queue.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	cfg      Config
	pageSize int64
}

func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.MaxEventBytes == 0 {
		cfg.MaxEventBytes = min(int64(1<<20), cfg.MaxStorageBytes*5/100)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o750); err != nil {
		return nil, fmt.Errorf("create queue directory: %w", err)
	}
	absPath, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve queue path: %w", err)
	}
	params := url.Values{
		"_busy_timeout": {"5000"},
		"_foreign_keys": {"on"},
		"_journal_mode": {"WAL"},
		"_synchronous":  {"FULL"},
		"_pragma": {
			"trusted_schema(OFF)",
			fmt.Sprintf("journal_size_limit(%d)", walBudget(cfg.MaxStorageBytes)),
			fmt.Sprintf("wal_autocheckpoint(%d)", max(int64(1), walBudget(cfg.MaxStorageBytes)/4096)),
		},
	}
	dsn := (&url.URL{Scheme: "file", Path: absPath, RawQuery: params.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite queue: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	cfg.Path = absPath
	store := &Store{db: db, cfg: cfg}
	if err := store.initialize(ctx); err != nil {
		return nil, errors.Join(err, closeDatabase(db))
	}
	if err := os.Chmod(absPath, 0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("protect queue database: %w", err), closeDatabase(db))
	}
	return store, nil
}

func closeDatabase(db *sql.DB) error {
	if err := db.Close(); err != nil {
		return fmt.Errorf("close queue database: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	return closeDatabase(s.db)
}
