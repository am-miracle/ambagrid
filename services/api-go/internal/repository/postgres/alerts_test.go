package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	execCalls  []execCall
	execErr    error
	committed  bool
	rolledBack bool
}

type execCall struct {
	sql  string
	args []any
}

func (tx *fakeTransaction) QueryRow(context.Context, string, ...any) pgx.Row {
	row := tx.rows[0]
	tx.rows = tx.rows[1:]
	return row
}

func (tx *fakeTransaction) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.execCalls = append(tx.execCalls, execCall{sql: sql, args: args})
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
	store := NewStoreWithOutboxTopics(pool, time.Second, OutboxTopics{
		AlertResolved: "custom.alert.resolved",
	})

	alert, err := store.ResolveAlert(context.Background(), command)
	if err != nil {
		t.Fatalf("ResolveAlert() error = %v", err)
	}
	if pool.options.IsoLevel != pgx.ReadCommitted {
		t.Fatalf("isolation = %v, want read committed", pool.options.IsoLevel)
	}
	if alert.Status != domain.AlertStatusResolved || alert.ResolvedAt == nil {
		t.Fatalf("alert = %+v, want resolved alert", alert)
	}
	if len(tx.execCalls) != 2 {
		t.Fatalf("exec calls = %d, want history and outbox inserts", len(tx.execCalls))
	}
	history := tx.execCalls[0]
	if history.sql != insertAlertResolutionSQL {
		t.Fatalf("history SQL = %q, want insertAlertResolutionSQL", history.sql)
	}
	if len(history.args) != 4 || history.args[0] != command.AlertID || history.args[2] != command.ResolutionNote || history.args[3] != command.ResolvedBy {
		t.Fatalf("history args = %#v", history.args)
	}
	outbox := tx.execCalls[1]
	if outbox.sql != insertCommandEventSQL {
		t.Fatalf("outbox SQL = %q, want insertCommandEventSQL", outbox.sql)
	}
	if len(outbox.args) != 5 || outbox.args[0] != "custom.alert.resolved" || outbox.args[1] != alertResolvedEventType || outbox.args[2] != "alert" || outbox.args[3] != command.AlertID {
		t.Fatalf("outbox args = %#v", outbox.args)
	}
	var payload map[string]any
	if err := json.Unmarshal(outbox.args[4].([]byte), &payload); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	if payload["alert_id"] != command.AlertID || payload["severity"] != "SEVERITY_CRITICAL" || payload["resolution_note"] != command.ResolutionNote || payload["resolved_by"] != command.ResolvedBy {
		t.Fatalf("payload = %#v", payload)
	}
	if !tx.committed {
		t.Fatal("transaction was not committed")
	}
}

func TestAlertResolvedPayloadMatchesSharedOutboxFixture(t *testing.T) {
	resolvedAt := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	resolutionNote := "fan cleaned"
	resolvedBy := "operator-0101"
	alert := domain.Alert{
		AlertID:        "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b",
		AssetID:        "battery-01",
		SiteID:         "site-01",
		Severity:       domain.SeverityCritical,
		Reason:         "temperature exceeded threshold",
		OpenedAt:       resolvedAt.Add(-time.Hour),
		ResolvedAt:     &resolvedAt,
		ResolutionNote: &resolutionNote,
		ResolvedBy:     &resolvedBy,
	}

	payload, err := alertResolvedPayload(alert)
	if err != nil {
		t.Fatalf("alertResolvedPayload() error = %v", err)
	}
	fixtureBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "proto", "fixtures", "alert_resolved_outbox_payload.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var want map[string]any
	if err := json.Unmarshal(fixtureBytes, &want); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload = %#v, want %#v", got, want)
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

func TestResolveAlertRollsBackWhenOutboxInsertFails(t *testing.T) {
	command := domain.ResolveAlertCommand{AlertID: "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b"}
	tx := &fakeTransaction{
		rows:    []pgx.Row{resolvedAlertRow(command)},
		execErr: nil,
	}
	txWithFailingSecondExec := &failingSecondExecTransaction{fakeTransaction: tx}
	pool := &fakeDatabasePool{tx: txWithFailingSecondExec}

	_, err := NewStore(pool, time.Second).ResolveAlert(context.Background(), command)

	if err == nil {
		t.Fatal("ResolveAlert() error = nil, want outbox insert error")
	}
	if tx.committed {
		t.Fatal("transaction with failed outbox insert was committed")
	}
	if !tx.rolledBack {
		t.Fatal("transaction with failed outbox insert was not rolled back")
	}
}

type failingSecondExecTransaction struct {
	*fakeTransaction
}

func (tx *failingSecondExecTransaction) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if len(tx.execCalls) == 1 {
		tx.execCalls = append(tx.execCalls, execCall{sql: sql, args: args})
		return pgconn.CommandTag{}, errors.New("outbox unavailable")
	}
	return tx.fakeTransaction.Exec(ctx, sql, args...)
}
