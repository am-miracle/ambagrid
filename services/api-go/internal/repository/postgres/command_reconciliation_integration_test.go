package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"api-go/internal/domain"
	"api-go/internal/repository"
	"api-go/internal/services"
)

func TestLegacyExpiryRetainsReportsWithoutChangingOutcome(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	id := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	if _, err := f.service.Poll(ctx, f.siteID, 0); err != nil {
		t.Fatal(err)
	}
	originalAt := time.Now().UTC().Add(time.Hour)
	for _, status := range []domain.MeterCommandStatus{domain.CommandStatusSent, domain.CommandStatusAcknowledged} {
		for range 2 {
			cmd, changed, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: status, OccurredAt: originalAt})
			if !errors.Is(err, services.ErrReportNeedsReconciliation) || changed || cmd.Status != domain.CommandStatusExpired {
				t.Fatalf("report %s: cmd=%+v changed=%t err=%v", status, cmd, changed, err)
			}
		}
	}
	if f.status(t, id) != domain.CommandStatusExpired {
		t.Fatal("conflicting evidence changed the settled status")
	}
	if events := f.outboxEvents(t, id); len(events) != 1 || events[0] != "meter.command.failed meter.command.failed expired" {
		t.Fatalf("conflicts must not publish new outcomes: %+v", events)
	}
	result, err := f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("reconciliation=%+v err=%v", result, err)
	}
	item := result.Items[0]
	if item.ConflictCount != 2 || item.LatestConflict == nil || item.LatestConflict.Status != domain.CommandStatusAcknowledged || !item.LatestConflict.OccurredAt.Equal(originalAt) || item.LastReportAt == nil || item.LatestConflictReceivedAt == nil {
		t.Fatalf("missing or duplicated evidence: %+v", item)
	}
	other, err := f.service.ListReconciliation(ctx, &f.other, 0, "")
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("cross-site evidence leak: %+v err=%v", other, err)
	}
}

func TestReconciliationPagesOverdueQueuedAndSentCommands(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	queued := f.insertCommand(t, f.meterID, domain.CommandDisconnectMeter, 3*time.Hour, time.Hour)
	sent := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 4*time.Hour, time.Hour)
	live := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, time.Minute, time.Hour)
	for _, id := range []string{queued, live} {
		if _, err := f.pool.Exec(ctx, "UPDATE meter_commands SET status='queued' WHERE command_id=$1::uuid", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: sent, Status: domain.CommandStatusSent, OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := f.pool.Exec(ctx, `INSERT INTO site_health(site_id,gateway_id,last_contact_at,last_event_timestamp) VALUES($1,'test:gateway',$2,$2)`, f.siteID, now); err != nil {
		t.Fatal(err)
	}
	first, err := f.service.ListReconciliation(ctx, &f.siteID, 1, "")
	if err != nil || len(first.Items) != 1 || first.Items[0].Command.CommandID != queued || first.NextCursor == "" || first.Items[0].Site.HealthStatus != domain.SiteHealthLive || first.Items[0].Site.LastContactAt == nil {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	second, err := f.service.ListReconciliation(ctx, &f.siteID, 1, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].Command.CommandID != sent || second.NextCursor != "" || second.Items[0].LastReportAt == nil {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
}

func TestExpirySweepSettlesOfflineUndeliveredCommandsOnly(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	first := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	other := f.insertCommand(t, f.otherMeterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	queued := f.insertCommand(t, f.meterID, domain.CommandDisconnectMeter, 2*time.Hour, time.Hour)
	sent := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	live := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, time.Minute, time.Hour)
	if _, err := f.pool.Exec(ctx, "UPDATE meter_commands SET status='queued' WHERE command_id=$1::uuid", queued); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: sent, Status: domain.CommandStatusSent, OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	count, err := f.service.SweepExpired(ctx)
	if err != nil || count != 2 {
		t.Fatalf("sweep count=%d err=%v", count, err)
	}
	for _, id := range []string{first, other} {
		if f.status(t, id) != domain.CommandStatusExpired || len(f.outboxEvents(t, id)) != 1 || len(f.audits(t, id)) != 1 {
			t.Fatalf("expiry not fully recorded for %s", id)
		}
	}
	if f.status(t, queued) != domain.CommandStatusQueued || f.status(t, sent) != domain.CommandStatusSent || f.status(t, live) != domain.CommandStatusRequested {
		t.Fatal("sweep changed a delivered or unexpired command")
	}
	if got := f.expiryPreviousStatus(t, first); got != "requested" {
		t.Fatalf("expiry audit previous_status = %q, want requested", got)
	}
	if count, err := f.service.SweepExpired(ctx); err != nil || count != 0 || len(f.outboxEvents(t, first)) != 1 {
		t.Fatalf("repeated sweep count=%d err=%v", count, err)
	}
}

func TestExpirySweepSkipsCommandsBeingDelivered(t *testing.T) {
	f := newEdgeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	store := NewEdgeCommandStore(f.pool, 5*time.Second)
	err := store.RunEdgeCommandTx(ctx, func(tx repository.EdgeCommandTx) error {
		commands, err := tx.ListPendingEdgeCommands(ctx, f.siteID, time.Now().Add(-90*time.Minute), 10)
		if err != nil || len(commands) != 1 {
			t.Fatalf("pending=%+v err=%v", commands, err)
		}
		if count, err := f.service.SweepExpired(ctx); err != nil || count != 0 {
			t.Fatalf("worker did not skip delivery's locked row: count=%d err=%v", count, err)
		}
		return tx.MarkCommandsQueued(ctx, []string{id})
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := f.service.SweepExpired(ctx); err != nil || count != 0 || f.status(t, id) != domain.CommandStatusQueued {
		t.Fatalf("delivered command expired: count=%d err=%v", count, err)
	}
}

func TestExpirySweepBoundsItsTransaction(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `
		INSERT INTO meter_commands(meter_id, command_type, requested_by, reason, requested_at, expires_at)
		SELECT $1, 'reconnect_meter', 'test:edge', 'batch limit', now()-interval '2 hours', now()-interval '1 hour'
		FROM generate_series(1,201)`, f.meterID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{200, 1, 0} {
		if got, err := f.service.SweepExpired(ctx); err != nil || got != want {
			t.Fatalf("batch=%d want=%d err=%v", got, want, err)
		}
	}
	var audits, outcomes int
	if err := f.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM audit_events WHERE site_id=$1 AND action='meter_command.expired'),
		(SELECT count(*) FROM command_event_outbox WHERE aggregate_id IN (SELECT command_id::text FROM meter_commands WHERE meter_id=$2))`, f.siteID, f.meterID).Scan(&audits, &outcomes); err != nil {
		t.Fatal(err)
	}
	if audits != 201 || outcomes != 201 {
		t.Fatalf("audits=%d outcomes=%d want one of each per command", audits, outcomes)
	}
}

func (f edgeFixture) expiryPreviousStatus(t *testing.T, commandID string) string {
	t.Helper()
	var status *string
	if err := f.pool.QueryRow(context.Background(), `SELECT metadata->>'previous_status' FROM audit_events
		WHERE subject_type = 'meter_command' AND subject_id = $1 AND action = 'meter_command.expired'`, commandID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == nil {
		return ""
	}
	return *status
}

// legacyExpire records an expiry the way platform code before the fix did:
// without previous_status, and possibly from queued.
func (f edgeFixture) legacyExpire(t *testing.T, commandID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, "UPDATE meter_commands SET status = 'expired' WHERE command_id = $1::uuid", commandID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO audit_events(site_id, actor_id, action, subject_type, subject_id, metadata)
		VALUES ($1, 'system:command-expiry', 'meter_command.expired', 'meter_command', $2, jsonb_build_object('expires_at', now()))`, f.siteID, commandID); err != nil {
		t.Fatal(err)
	}
}

func TestPollRecordsWhichStateItExpiredFrom(t *testing.T) {
	f := newEdgeFixture(t)
	id := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	if _, err := f.service.Poll(context.Background(), f.siteID, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.expiryPreviousStatus(t, id); got != "requested" {
		t.Fatalf("expiry audit previous_status = %q, want requested", got)
	}
}

func TestReconciliationSurfacesExpiriesOverturnedByLegacyReports(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	overturned := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	stillExpired := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	f.legacyExpire(t, overturned)
	f.legacyExpire(t, stillExpired)
	// Older platform code let the edge's late acknowledgement overturn the expiry,
	// after meter.command.failed had already been published.
	if _, err := f.pool.Exec(ctx, `UPDATE meter_commands SET status = 'acknowledged', sent_at = now(), acknowledged_at = now()
		WHERE command_id = $1::uuid`, overturned); err != nil {
		t.Fatal(err)
	}
	fixed := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	if _, err := f.service.Poll(ctx, f.siteID, 0); err != nil {
		t.Fatal(err)
	}
	if f.status(t, fixed) != domain.CommandStatusExpired {
		t.Fatal("setup: current code did not expire the undelivered command")
	}

	result, err := f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("reconciliation = %+v err %v; want only the overturned expiry", result, err)
	}
	item := result.Items[0]
	if item.Command.CommandID != overturned || item.Command.Status != domain.CommandStatusAcknowledged ||
		item.LegacyExpiredAt == nil || item.Reason() != domain.ReconciliationOverturnedExpiry {
		t.Fatalf("item = %+v", item)
	}

	// The rollout case: a falsely expired row whose acknowledgement arrives now.
	if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: stillExpired, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now()}); !errors.Is(err, services.ErrReportNeedsReconciliation) {
		t.Fatalf("late acknowledgement err = %v", err)
	}
	result, err = f.service.ListReconciliation(ctx, &f.siteID, 0, "")
	if err != nil || len(result.Items) != 2 || result.Items[1].Command.CommandID != stillExpired {
		t.Fatalf("reconciliation = %+v err %v; want the conflicting legacy expiry too", result, err)
	}
	if item := result.Items[1]; item.Reason() != domain.ReconciliationConflictingReport || item.LegacyExpiredAt == nil {
		t.Fatalf("legacy conflict = %+v; want conflicting_report with its legacy expiry time", item)
	}
}

func TestReconciliationListsEverySiteUnlessFiltered(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	mine := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	theirs := f.insertCommand(t, f.otherMeterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	if _, err := f.pool.Exec(ctx, "UPDATE meter_commands SET status = 'queued' WHERE command_id = ANY($1::uuid[])", []string{mine, theirs}); err != nil {
		t.Fatal(err)
	}
	found := func(siteID *string) map[string]string {
		t.Helper()
		sites := map[string]string{}
		cursor := ""
		for {
			result, err := f.service.ListReconciliation(ctx, siteID, 200, cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range result.Items {
				if item.Command.CommandID == mine || item.Command.CommandID == theirs {
					sites[item.Command.CommandID] = item.SiteID
				}
			}
			if result.NextCursor == "" {
				return sites
			}
			cursor = result.NextCursor
		}
	}
	if got := found(nil); got[mine] != f.siteID || got[theirs] != f.other {
		t.Fatalf("fleet-wide listing = %v; want both sites' commands", got)
	}
	if got := found(&f.other); len(got) != 1 || got[theirs] != f.other {
		t.Fatalf("filtered listing = %v; want only %s", got, f.other)
	}
}
