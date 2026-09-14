package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"api-go/internal/domain"
)

type fakeDatabasePool struct {
	tx      pgx.Tx
	options pgx.TxOptions
}

func (p *fakeDatabasePool) BeginTx(_ context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	p.options = options
	return p.tx, nil
}

func (*fakeDatabasePool) Ping(context.Context) error { return nil }

func (*fakeDatabasePool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query call")
}

func (*fakeDatabasePool) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("unexpected QueryRow call")
}

type fakeTransaction struct {
	pgx.Tx
	rows       []pgx.Row
	execSQL    string
	execArgs   []any
	execErr    error
	committed  bool
	rolledBack bool
}

func (tx *fakeTransaction) QueryRow(context.Context, string, ...any) pgx.Row {
	row := tx.rows[0]
	tx.rows = tx.rows[1:]
	return row
}

func (tx *fakeTransaction) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.execSQL = sql
	tx.execArgs = args
	return pgconn.NewCommandTag("INSERT 0 1"), tx.execErr
}

func (tx *fakeTransaction) Commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *fakeTransaction) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}

type fakeRow func(dest ...any) error

func (row fakeRow) Scan(dest ...any) error { return row(dest...) }

func resolvedAlertRow(command domain.ResolveAlertCommand) pgx.Row {
	return fakeRow(func(dest ...any) error {
		resolvedAt := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
		assetID := "battery-01"
		siteID := "site-01"
		kind := "battery_overheat"
		severity := string(domain.SeverityCritical)
		status := string(domain.AlertStatusResolved)
		reason := "temperature exceeded threshold"
		openedAt := resolvedAt.Add(-time.Hour)
		*dest[0].(*string) = command.AlertID
		*dest[1].(*string) = assetID
		*dest[2].(*string) = siteID
		*dest[3].(*string) = kind
		*dest[4].(*string) = severity
		*dest[5].(*string) = status
		*dest[6].(*string) = reason
		*dest[7].(*time.Time) = openedAt
		*dest[8].(**string) = nil
		*dest[9].(**time.Time) = &resolvedAt
		*dest[10].(**string) = &command.ResolutionNote
		*dest[11].(**string) = &command.ResolvedBy
		return nil
	})
}

func errorRow(err error) pgx.Row {
	return fakeRow(func(...any) error { return err })
}

func existenceRow(exists bool) pgx.Row {
	return fakeRow(func(dest ...any) error {
		*dest[0].(*bool) = exists
		return nil
	})
}

func TestResolveAlertUpdatesAndRecordsHistoryAtomically(t *testing.T) {
	command := domain.ResolveAlertCommand{
		AlertID:        "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b",
		ResolutionNote: "fan cleaned",
		ResolvedBy:     "operator-0101",
	}
	tx := &fakeTransaction{rows: []pgx.Row{resolvedAlertRow(command)}}
	pool := &fakeDatabasePool{tx: tx}

	alert, err := NewStore(pool, time.Second).ResolveAlert(context.Background(), command)
	if err != nil {
		t.Fatalf("ResolveAlert() error = %v", err)
	}
	if pool.options.IsoLevel != pgx.ReadCommitted {
		t.Fatalf("isolation = %v, want read committed", pool.options.IsoLevel)
	}
	if alert.Status != domain.AlertStatusResolved || alert.ResolvedAt == nil {
		t.Fatalf("alert = %+v, want resolved alert", alert)
	}
	if tx.execSQL != insertAlertResolutionSQL {
		t.Fatalf("history SQL = %q, want insertAlertResolutionSQL", tx.execSQL)
	}
	if len(tx.execArgs) != 4 || tx.execArgs[0] != command.AlertID || tx.execArgs[2] != command.ResolutionNote || tx.execArgs[3] != command.ResolvedBy {
		t.Fatalf("history args = %#v", tx.execArgs)
	}
	if !tx.committed {
		t.Fatal("transaction was not committed")
	}
}

func TestResolveAlertDistinguishesMissingAndClosedAlerts(t *testing.T) {
	tests := map[string]struct {
		exists  bool
		wantErr error
	}{
		"missing": {exists: false, wantErr: domain.ErrNotFound},
		"closed":  {exists: true, wantErr: domain.ErrConflict},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			tx := &fakeTransaction{rows: []pgx.Row{errorRow(pgx.ErrNoRows), existenceRow(test.exists)}}
			pool := &fakeDatabasePool{tx: tx}

			_, err := NewStore(pool, time.Second).ResolveAlert(context.Background(), domain.ResolveAlertCommand{AlertID: "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b"})

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ResolveAlert() error = %v, want %v", err, test.wantErr)
			}
			if tx.committed {
				t.Fatal("failed transaction was committed")
			}
			if !tx.rolledBack {
				t.Fatal("failed transaction was not rolled back")
			}
		})
	}
}

func TestResolveAlertRollsBackWhenHistoryInsertFails(t *testing.T) {
	command := domain.ResolveAlertCommand{AlertID: "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b"}
	tx := &fakeTransaction{
		rows:    []pgx.Row{resolvedAlertRow(command)},
		execErr: errors.New("history unavailable"),
	}
	pool := &fakeDatabasePool{tx: tx}

	_, err := NewStore(pool, time.Second).ResolveAlert(context.Background(), command)

	if err == nil {
		t.Fatal("ResolveAlert() error = nil, want history insert error")
	}
	if tx.committed {
		t.Fatal("transaction with failed history insert was committed")
	}
	if !tx.rolledBack {
		t.Fatal("transaction with failed history insert was not rolled back")
	}
}
