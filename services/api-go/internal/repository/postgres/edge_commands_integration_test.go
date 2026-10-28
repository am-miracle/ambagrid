package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"api-go/internal/domain"
	"api-go/internal/services"
)

type edgeFixture struct {
	pool         *pgxpool.Pool
	service      *services.EdgeCommandService
	siteID       string
	other        string
	meterID      string
	assetID      string
	otherMeterID string
}

func newEdgeFixture(t *testing.T) edgeFixture {
	t.Helper()
	pool := integrationPool(t)
	ctx := context.Background()

	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	f := edgeFixture{
		pool: pool, siteID: "it-edge-site-" + suffix, other: "it-edge-other-" + suffix,
		meterID: "it-edge-meter-" + suffix, assetID: "it-edge-asset-" + suffix,
	}
	otherMeter, otherAsset := "it-edge-other-meter-"+suffix, "it-edge-other-asset-"+suffix
	f.otherMeterID = otherMeter
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed database: %v", err)
		}
	}
	for _, site := range []struct{ site, asset, meter string }{{f.siteID, f.assetID, f.meterID}, {f.other, otherAsset, otherMeter}} {
		mustExec("INSERT INTO sites (site_id, name) VALUES ($1, 'Edge command integration test')", site.site)
		mustExec("INSERT INTO assets (asset_id, site_id, asset_type, last_seen_at) VALUES ($1, $2, 'smart_meter', now())", site.asset, site.site)
		mustExec("INSERT INTO smart_meters (meter_id, site_id, asset_id) VALUES ($1, $2, $3)", site.meter, site.site, site.asset)
	}
	clean := cleaner(t, pool)
	t.Cleanup(func() {
		meters := []string{f.meterID, otherMeter}
		clean("DELETE FROM audit_events WHERE site_id = ANY($1)", []string{f.siteID, f.other})
		clean("DELETE FROM command_event_outbox WHERE aggregate_type = 'meter_command' AND aggregate_id IN (SELECT command_id::text FROM meter_commands WHERE meter_id = ANY($1))", meters)
		clean("DELETE FROM meter_commands WHERE meter_id = ANY($1)", meters)
		clean("DELETE FROM smart_meters WHERE meter_id = ANY($1)", meters)
		clean("DELETE FROM assets WHERE asset_id = ANY($1)", []string{f.assetID, otherAsset})
		clean("DELETE FROM sites WHERE site_id = ANY($1)", []string{f.siteID, f.other})
	})
	f.service = services.NewEdgeCommandService(NewEdgeCommandStore(pool, 5*time.Second), services.OutcomeTopics{}, services.SiteHealthThresholds{OfflineAfter: 5 * time.Minute, EventStaleAfter: 5 * time.Minute}, services.PageLimits{DefaultSize: 50, MaxSize: 200})
	return f
}

// insertCommand creates a command requested ago and deliverable for ttl.
func (f edgeFixture) insertCommand(t *testing.T, meterID string, commandType domain.MeterCommandType, ago, ttl time.Duration) string {
	t.Helper()
	var id string
	err := f.pool.QueryRow(context.Background(), `
		INSERT INTO meter_commands (meter_id, command_type, requested_by, reason, requested_at, expires_at)
		VALUES ($1, $2, 'test:edge', 'integration test', now() - $3::interval, now() - $3::interval + $4::interval)
		RETURNING command_id::text`, meterID, commandType, ago.String(), ttl.String()).Scan(&id)
	if err != nil {
		t.Fatalf("insert command: %v", err)
	}
	return id
}

func (f edgeFixture) status(t *testing.T, commandID string) domain.MeterCommandStatus {
	t.Helper()
	var status domain.MeterCommandStatus
	if err := f.pool.QueryRow(context.Background(), "SELECT status FROM meter_commands WHERE command_id = $1::uuid", commandID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func (f edgeFixture) audits(t *testing.T, commandID string) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), "SELECT action FROM audit_events WHERE subject_type = 'meter_command' AND subject_id = $1 ORDER BY occurred_at, audit_event_id", commandID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
	}
	return actions
}

func TestEdgePollDeliversOwnSitesCommandsInOrderAndExpiresOverdue(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	older := f.insertCommand(t, f.meterID, domain.CommandDisconnectMeter, 2*time.Hour, 24*time.Hour)
	newer := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, time.Hour, 24*time.Hour)
	overdue := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 3*time.Hour, time.Hour)
	f.insertCommand(t, f.otherMeterID, domain.CommandReconnectMeter, time.Hour, 24*time.Hour)

	commands, err := f.service.Poll(ctx, f.siteID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[0].CommandID != older || commands[1].CommandID != newer {
		t.Fatalf("polled %+v, want the two live commands of this site oldest first", commands)
	}
	if commands[0].DeviceID != f.assetID || commands[0].Status != domain.CommandStatusQueued || !commands[0].ExpiresAt.After(time.Now()) ||
		// The database and test clocks may differ slightly, so the bound is loose.
		commands[0].ExpiresIn < 21*time.Hour || commands[0].ExpiresIn > 23*time.Hour {
		t.Fatalf("first command = %+v", commands[0])
	}
	if got := f.status(t, older); got != domain.CommandStatusQueued {
		t.Fatalf("delivered command status = %q, want queued", got)
	}
	if got := f.status(t, overdue); got != domain.CommandStatusExpired {
		t.Fatalf("overdue command status = %q, want expired", got)
	}
	if got := f.audits(t, overdue); len(got) != 1 || got[0] != "meter_command.expired" {
		t.Fatalf("overdue command audit = %v", got)
	}

	if again, err := f.service.Poll(ctx, f.siteID, 1); err != nil || len(again) != 1 || again[0].CommandID != older {
		t.Fatalf("second poll with limit 1 = %+v, %v; want the oldest command redelivered", again, err)
	}
}

func TestEdgeReportsMoveCommandsThroughTheirLifecycle(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	id := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, time.Minute, 24*time.Hour)
	// In the past, so the platform's clamp to its own now leaves both times intact.
	sentAt := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Millisecond)
	report := func(status domain.MeterCommandStatus, at time.Time, reason string) (domain.MeterCommand, bool, error) {
		return f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: status, OccurredAt: at, FailureReason: reason})
	}

	if cmd, changed, err := report(domain.CommandStatusSent, sentAt, ""); err != nil || !changed || cmd.Status != domain.CommandStatusSent {
		t.Fatalf("sent = %+v changed %t err %v", cmd, changed, err)
	}
	if _, changed, err := report(domain.CommandStatusSent, sentAt, ""); err != nil || changed {
		t.Fatalf("repeated sent changed %t err %v, want an idempotent no-op", changed, err)
	}
	cmd, changed, err := report(domain.CommandStatusAcknowledged, sentAt.Add(2*time.Second), "")
	if err != nil || !changed || cmd.Status != domain.CommandStatusAcknowledged {
		t.Fatalf("acknowledged = %+v changed %t err %v", cmd, changed, err)
	}
	var storedSent, storedAck time.Time
	if err := f.pool.QueryRow(ctx, "SELECT sent_at, acknowledged_at FROM meter_commands WHERE command_id = $1::uuid", id).Scan(&storedSent, &storedAck); err != nil {
		t.Fatal(err)
	}
	if !storedSent.Equal(sentAt) || !storedAck.Equal(sentAt.Add(2*time.Second)) {
		t.Fatalf("stored sent %s ack %s", storedSent, storedAck)
	}
	if _, _, err := report(domain.CommandStatusFailed, sentAt, "late failure"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("failure after acknowledgement err = %v, want ErrConflict", err)
	}
	if got := f.audits(t, id); len(got) != 3 || got[0] != "meter_command.sent" || got[1] != "meter_command.acknowledged" || got[2] != "meter_command.report_conflict" {
		t.Fatalf("audit trail = %v", got)
	}
	if polled, err := f.service.Poll(ctx, f.siteID, 0); err != nil || len(polled) != 0 {
		t.Fatalf("poll after acknowledgement = %+v, %v; want nothing pending", polled, err)
	}
}

func TestEdgeReportsRecordFailureAndRespectSiteBoundaries(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	id := f.insertCommand(t, f.meterID, domain.CommandDisconnectMeter, time.Minute, 24*time.Hour)

	_, _, err := f.service.Report(ctx, f.other, domain.CommandReport{CommandID: id, Status: domain.CommandStatusSent, OccurredAt: time.Now()})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("report from another site err = %v, want ErrNotFound", err)
	}
	cmd, changed, err := f.service.Report(ctx, f.siteID, domain.CommandReport{
		CommandID: id, Status: domain.CommandStatusFailed, OccurredAt: time.Now(), FailureReason: "superseded by a newer relay command",
	})
	if err != nil || !changed || cmd.Status != domain.CommandStatusFailed {
		t.Fatalf("failed = %+v changed %t err %v", cmd, changed, err)
	}
	var reason string
	if err := f.pool.QueryRow(ctx, "SELECT failure_reason FROM meter_commands WHERE command_id = $1::uuid", id).Scan(&reason); err != nil || reason != "superseded by a newer relay command" {
		t.Fatalf("stored failure reason %q, err %v", reason, err)
	}
}

func TestEdgePollLeavesOverdueDeliveredCommandsForTheEdgeToSettle(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	id := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, 24*time.Hour)
	if _, err := f.service.Poll(ctx, f.siteID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE meter_commands SET expires_at = requested_at + interval '1 hour' WHERE command_id = $1::uuid", id); err != nil {
		t.Fatal(err)
	}

	polled, err := f.service.Poll(ctx, f.siteID, 0)
	if err != nil || len(polled) != 1 || polled[0].CommandID != id || polled[0].Status != domain.CommandStatusQueued || polled[0].ExpiresIn > 0 {
		t.Fatalf("poll = %+v, %v; want the overdue queued command redelivered with no time left", polled, err)
	}
	if got := f.outboxEvents(t, id); len(got) != 0 {
		t.Fatalf("overdue queued command published %v, want nothing until the edge reports", got)
	}

	cmd, changed, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now()})
	if err != nil || !changed || cmd.Status != domain.CommandStatusAcknowledged {
		t.Fatalf("acknowledging an overdue queued command = %+v changed %t err %v", cmd, changed, err)
	}
	if got := f.outboxEvents(t, id); !reflect.DeepEqual(got, []string{"meter.command.acknowledged meter.command.acknowledged acknowledged"}) {
		t.Fatalf("outcome events = %v, want only the acknowledgement", got)
	}
}

func TestEdgeReportCannotOverturnPlatformExpiry(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	id := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, time.Hour)
	if _, err := f.service.Poll(ctx, f.siteID, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, id); got != domain.CommandStatusExpired {
		t.Fatalf("status before report = %q, want expired", got)
	}
	_, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now()})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("acknowledging an expired command err = %v, want ErrConflict", err)
	}
	if got := f.outboxEvents(t, id); !reflect.DeepEqual(got, []string{"meter.command.failed meter.command.failed expired"}) {
		t.Fatalf("outcome events = %v, want only the expiry", got)
	}
}

func TestEdgePollStopsDeliveringSentCommandsAfterExpiryWithoutExpiringThem(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	id := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Hour, 24*time.Hour)
	if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: domain.CommandStatusSent, OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE meter_commands SET expires_at = requested_at + interval '1 hour' WHERE command_id = $1::uuid", id); err != nil {
		t.Fatal(err)
	}

	polled, err := f.service.Poll(ctx, f.siteID, 0)
	if err != nil || len(polled) != 0 {
		t.Fatalf("poll = %+v, %v; want an overdue sent command withheld", polled, err)
	}
	// The device may have applied it, so it stays sent for reconciliation.
	if got := f.status(t, id); got != domain.CommandStatusSent {
		t.Fatalf("status = %q, want sent", got)
	}
}

func TestMeterModePaymentDeliversItsCreditToTheEdgeOnce(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	customerID, tariffID := f.siteID+"-customer", f.siteID+"-tariff"
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed database: %v", err)
		}
	}
	mustExec("UPDATE smart_meters SET credit_mode = 'meter' WHERE meter_id = $1", f.meterID)
	mustExec("INSERT INTO customers (customer_id, site_id, display_name) VALUES ($1, $2, 'Meter credit customer')", customerID, f.siteID)
	mustExec("INSERT INTO meter_assignments (site_id, meter_id, customer_id) VALUES ($1, $2, $3)", f.siteID, f.meterID, customerID)
	mustExec("INSERT INTO tariff_plans (tariff_plan_id, site_id, name, currency, price_per_kwh, effective_from) VALUES ($1, $2, 'Credit tariff', 'NGN', 250, now() - interval '1 day')", tariffID, f.siteID)
	clean := cleaner(t, f.pool)
	t.Cleanup(func() {
		clean("DELETE FROM command_event_outbox WHERE aggregate_id IN (SELECT command_id::text FROM meter_commands WHERE meter_id = $1) OR aggregate_id IN (SELECT payment_id::text FROM payments WHERE customer_id = $2) OR aggregate_id IN (SELECT credit_id::text FROM energy_credits WHERE site_id = $3)", f.meterID, customerID, f.siteID)
		clean("DELETE FROM audit_events WHERE site_id = $1", f.siteID)
		clean("DELETE FROM meter_commands WHERE meter_id = $1", f.meterID)
		clean("UPDATE credit_balances SET remaining_kwh = 0, remaining_money_value_minor_units = 0 WHERE assignment_id IN (SELECT assignment_id FROM meter_assignments WHERE customer_id = $1)", customerID)
		clean("DELETE FROM credit_balances WHERE assignment_id IN (SELECT assignment_id FROM meter_assignments WHERE customer_id = $1)", customerID)
		clean("DELETE FROM energy_credits WHERE site_id = $1", f.siteID)
		clean("DELETE FROM payments WHERE customer_id = $1", customerID)
		clean("DELETE FROM meter_assignments WHERE customer_id = $1", customerID)
		clean("DELETE FROM tariff_plans WHERE tariff_plan_id = $1", tariffID)
		clean("DELETE FROM customers WHERE customer_id = $1", customerID)
	})

	payments := services.NewPaymentService(NewStore(f.pool, 5*time.Second))
	payment := domain.ApplyPaymentCommand{
		Provider: "integration", ExternalReference: f.siteID + "-ref", CustomerID: customerID,
		AmountMinorUnits: 500000, Currency: "NGN", Status: domain.PaymentStatusConfirmed, ConfirmedAt: time.Now().UTC(),
	}
	result, err := payments.ApplyWebhookPayment(ctx, payment)
	if err != nil {
		t.Fatal(err)
	}
	cmd := result.MeterCommand
	if cmd == nil || cmd.CommandType != domain.CommandCreditMeter || cmd.CreditKWh == nil || *cmd.CreditKWh != result.Credit.KWhGranted || cmd.CreditID == nil || *cmd.CreditID != result.Credit.CreditID {
		t.Fatalf("meter command = %+v, want a credit for %v kWh from credit %s", cmd, result.Credit.KWhGranted, result.Credit.CreditID)
	}
	replay, err := payments.ApplyWebhookPayment(ctx, payment)
	if err != nil || replay.MeterCommand == nil || replay.MeterCommand.CommandID != cmd.CommandID || *replay.MeterCommand.CreditKWh != *cmd.CreditKWh {
		t.Fatalf("replay = %+v, %v; want the same credit command", replay.MeterCommand, err)
	}

	var payload map[string]any
	var raw []byte
	if err := f.pool.QueryRow(ctx, "SELECT payload FROM command_event_outbox WHERE event_type = 'meter.command.requested' AND aggregate_id = $1", cmd.CommandID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["credit_kwh"] != *cmd.CreditKWh || payload["credit_id"] != *cmd.CreditID || payload["expires_at"] == nil {
		t.Fatalf("requested event payload = %v", payload)
	}

	polled, err := f.service.Poll(ctx, f.siteID, 0)
	if err != nil || len(polled) != 1 || polled[0].CreditKWh == nil || *polled[0].CreditKWh != *cmd.CreditKWh {
		t.Fatalf("poll = %+v, %v; want the credit command with its amount", polled, err)
	}
}

func (f edgeFixture) outboxEvents(t *testing.T, commandID string) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), "SELECT topic || ' ' || event_type || ' ' || (payload->>'status') FROM command_event_outbox WHERE aggregate_type = 'meter_command' AND aggregate_id = $1 ORDER BY recorded_at, event_id", commandID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var event string
		if err := rows.Scan(&event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func TestEdgeOutcomesArePublishedOnceThroughTheOutbox(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	acked := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 2*time.Minute, 24*time.Hour)
	failed := f.insertCommand(t, f.meterID, domain.CommandDisconnectMeter, time.Minute, 24*time.Hour)
	overdue := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, 3*time.Hour, time.Hour)
	report := func(id string, status domain.MeterCommandStatus, reason string) {
		t.Helper()
		if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: status, OccurredAt: time.Now(), FailureReason: reason}); err != nil {
			t.Fatalf("report %s: %v", status, err)
		}
	}

	report(acked, domain.CommandStatusSent, "")
	if got := f.outboxEvents(t, acked); len(got) != 0 {
		t.Fatalf("sent published %v, want nothing", got)
	}
	report(acked, domain.CommandStatusAcknowledged, "")
	report(acked, domain.CommandStatusAcknowledged, "")
	if got := f.outboxEvents(t, acked); !reflect.DeepEqual(got, []string{"meter.command.acknowledged meter.command.acknowledged acknowledged"}) {
		t.Fatalf("acknowledged events = %v, want exactly one", got)
	}
	report(failed, domain.CommandStatusFailed, "superseded by newer relay command")
	if got := f.outboxEvents(t, failed); !reflect.DeepEqual(got, []string{"meter.command.failed meter.command.failed failed"}) {
		t.Fatalf("failed events = %v", got)
	}
	if _, err := f.service.Poll(ctx, f.siteID, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.outboxEvents(t, overdue); !reflect.DeepEqual(got, []string{"meter.command.failed meter.command.failed expired"}) {
		t.Fatalf("expiry events = %v", got)
	}
}

func TestEdgeReportTimesAreClampedToPlausibleBounds(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	id := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, time.Minute, 24*time.Hour)
	// A gateway clock a day behind would otherwise record a send before the request.
	if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: domain.CommandStatusSent, OccurredAt: time.Now().Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.Report(ctx, f.siteID, domain.CommandReport{CommandID: id, Status: domain.CommandStatusAcknowledged, OccurredAt: time.Now().Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var requested, sent, acked time.Time
	if err := f.pool.QueryRow(ctx, "SELECT requested_at, sent_at, acknowledged_at FROM meter_commands WHERE command_id = $1::uuid", id).Scan(&requested, &sent, &acked); err != nil {
		t.Fatal(err)
	}
	if sent.Before(requested) || acked.After(time.Now().Add(time.Second)) || acked.Before(sent) {
		t.Fatalf("requested %s sent %s acknowledged %s; want requested <= sent <= acknowledged <= now", requested, sent, acked)
	}
}

func TestEdgePollSentBacklogDoesNotBlockLaterDelivery(t *testing.T) {
	f := newEdgeFixture(t)
	ctx := context.Background()
	// A full default batch has already reached devices but has no final outcome.
	var sent []string
	for i := range 50 {
		id := f.insertCommand(t, f.meterID, domain.CommandDisconnectMeter, time.Duration(100-i)*time.Minute, 24*time.Hour)
		if _, err := f.pool.Exec(ctx, "UPDATE meter_commands SET status='sent', sent_at=now() WHERE command_id=$1::uuid", id); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, id)
	}
	// A credit awaiting a send and a newly requested relay command must both
	// reach the edge despite the older unresolved commands.
	var credit string
	if err := f.pool.QueryRow(ctx, `INSERT INTO meter_commands
		(meter_id,command_type,status,requested_by,reason,requested_at,expires_at,credit_kwh)
		VALUES ($1,'credit_meter','queued','test:edge','integration test',now()-interval '2 minutes',now()+interval '1 hour',10)
		RETURNING command_id::text`, f.meterID).Scan(&credit); err != nil {
		t.Fatal(err)
	}
	relay := f.insertCommand(t, f.meterID, domain.CommandReconnectMeter, time.Minute, time.Hour)
	commands, err := f.service.Poll(ctx, f.siteID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 50 || commands[0].CommandID != credit || commands[1].CommandID != relay {
		t.Fatalf("poll returned %d commands; want %s and %s first in a full default batch", len(commands), credit, relay)
	}
	if commands[0].CreditKWh == nil || *commands[0].CreditKWh != 10 || commands[1].Status != domain.CommandStatusQueued || f.status(t, relay) != domain.CommandStatusQueued {
		t.Fatalf("delivery lost amount or queued status: %+v", commands[:2])
	}
	for i, cmd := range commands[2:] {
		if cmd.CommandID != sent[i] || cmd.Status != domain.CommandStatusSent {
			t.Fatalf("retry %d = %+v; want oldest sent command %s", i, cmd, sent[i])
		}
	}
	if again, err := f.service.Poll(ctx, f.siteID, 1); err != nil || len(again) != 1 || again[0].CommandID != credit {
		t.Fatalf("poll with limit 1 = %+v, %v; want credit ahead of sent backlog", again, err)
	}
}
