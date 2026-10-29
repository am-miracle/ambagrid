package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
)

func TestCommandsChangeMeterState(t *testing.T) {
	a := New("site-01")

	result := execute(t, a, adapter.Command{ID: "off", Type: adapter.CommandDisconnect, DeviceID: meterID})
	if result.RelayClosed == nil || *result.RelayClosed {
		t.Fatalf("disconnect relay = %v, want open", result.RelayClosed)
	}
	if meter := readMeter(t, a); meter.RelayClosed {
		t.Fatal("telemetry reports relay closed after disconnect")
	}
	execute(t, a, adapter.Command{ID: "on", Type: adapter.CommandReconnect, DeviceID: meterID})
	if meter := readMeter(t, a); !meter.RelayClosed {
		t.Fatal("telemetry reports relay open after reconnect")
	}

	execute(t, a, adapter.Command{ID: "credit-1", Type: adapter.CommandCredit, DeviceID: meterID, CreditKWh: 12.5})
	execute(t, a, adapter.Command{ID: "credit-2", Type: adapter.CommandCredit, DeviceID: meterID, CreditKWh: 2.5})
	status := execute(t, a, adapter.Command{ID: "status", Type: adapter.CommandRequestStatus, DeviceID: meterID})
	if status.BalanceKWh == nil || *status.BalanceKWh != 15 {
		t.Fatalf("balance = %v, want 15 kWh", status.BalanceKWh)
	}
	if status.RelayClosed == nil || !*status.RelayClosed {
		t.Fatalf("status relay = %v, want closed", status.RelayClosed)
	}
}

func TestTamperIsReportedInTelemetryAndStatus(t *testing.T) {
	a := New("site-01")
	for _, tamper := range []bool{true, false} {
		a.Device().SetTamper(tamper)
		if meter := readMeter(t, a); meter.TamperDetected != tamper {
			t.Fatalf("telemetry tamper = %t, want %t", meter.TamperDetected, tamper)
		}
		status := execute(t, a, adapter.Command{ID: fmt.Sprintf("status-%t", tamper), Type: adapter.CommandRequestStatus, DeviceID: meterID})
		if status.TamperDetected == nil || *status.TamperDetected != tamper {
			t.Fatalf("status tamper = %v, want %t", status.TamperDetected, tamper)
		}
	}
}

func TestOfflineMeterFreezesLastSeenAndTimesOutCommands(t *testing.T) {
	a := New("site-01")
	clock := time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)
	a.device.now = func() time.Time { return clock }

	online := readMeter(t, a)
	if !online.LastSeenAt.Equal(online.TakenAt) {
		t.Fatalf("online meter last seen %s, taken at %s", online.LastSeenAt, online.TakenAt)
	}

	a.Device().SetMeterOnline(false)
	clock = clock.Add(time.Minute)
	stale := readMeter(t, a)
	if !stale.TakenAt.Equal(clock) || !stale.LastSeenAt.Equal(online.LastSeenAt) {
		t.Fatalf("offline meter taken at %s last seen %s, want %s and %s", stale.TakenAt, stale.LastSeenAt, clock, online.LastSeenAt)
	}
	var payload canonicalPayload
	if err := json.Unmarshal(stale.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TimestampUTC != clock.Unix() || payload.LastSeenUTC != online.LastSeenAt.Unix() {
		t.Fatalf("offline meter payload timestamp_utc %d last_seen_utc %d, want %d and %d", payload.TimestampUTC, payload.LastSeenUTC, clock.Unix(), online.LastSeenAt.Unix())
	}

	credit := adapter.Command{ID: "credit", Type: adapter.CommandCredit, DeviceID: meterID, CreditKWh: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := a.Execute(ctx, credit); !errors.Is(err, adapter.ErrTimeout) {
		t.Fatalf("command to offline meter error = %v, want ErrTimeout", err)
	}
	if n := len(a.Device().Received()); n != 0 {
		t.Fatalf("offline meter applied %d commands", n)
	}

	a.Device().SetMeterOnline(true)
	if result := execute(t, a, credit); result.BalanceKWh == nil || *result.BalanceKWh != 1 {
		t.Fatalf("retried credit balance = %v, want 1 kWh", result.BalanceKWh)
	}
	if recovered := readMeter(t, a); !recovered.LastSeenAt.Equal(clock) {
		t.Fatalf("recovered meter last seen %s, want %s", recovered.LastSeenAt, clock)
	}
}

func TestMeterOfflineBeforeFirstPollIsOmitted(t *testing.T) {
	a := New("site-01")
	a.Device().SetMeterOnline(false)
	readings, err := a.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range readings {
		if r.DeviceID == meterID {
			t.Fatalf("gateway reported a meter it never heard from: %+v", r)
		}
	}
}

func execute(t *testing.T, a *Adapter, cmd adapter.Command) adapter.CommandResult {
	t.Helper()
	result, err := a.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("execute %s: %v", cmd.ID, err)
	}
	return result
}

func readMeter(t *testing.T, a *Adapter) domain.Reading {
	t.Helper()
	readings, err := a.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range readings {
		if r.DeviceID == meterID {
			return r
		}
	}
	t.Fatal("no meter reading")
	return domain.Reading{}
}
