package domain

import (
	"errors"
	"testing"
	"time"
)

const reportCommandID = "3f2b8c1e-5d4a-4e6b-9c7d-1a2b3c4d5e6f"

func TestCommandReportValidate(t *testing.T) {
	at := time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		report CommandReport
		valid  bool
	}{
		"sent":               {CommandReport{CommandID: reportCommandID, Status: CommandStatusSent, OccurredAt: at}, true},
		"failed with reason": {CommandReport{CommandID: reportCommandID, Status: CommandStatusFailed, OccurredAt: at, FailureReason: "superseded"}, true},
		"failed without":     {CommandReport{CommandID: reportCommandID, Status: CommandStatusFailed, OccurredAt: at}, false},
		"reason when sent":   {CommandReport{CommandID: reportCommandID, Status: CommandStatusSent, OccurredAt: at, FailureReason: "x"}, false},
		"requested":          {CommandReport{CommandID: reportCommandID, Status: CommandStatusRequested, OccurredAt: at}, false},
		"missing time":       {CommandReport{CommandID: reportCommandID, Status: CommandStatusAcknowledged}, false},
		"not a uuid":         {CommandReport{CommandID: "cmd-1", Status: CommandStatusSent, OccurredAt: at}, false},
		"oversized reason":   {CommandReport{CommandID: reportCommandID, Status: CommandStatusFailed, OccurredAt: at, FailureReason: string(make([]byte, 501))}, false},
	} {
		if err := c.report.Validate(); (err == nil) != c.valid {
			t.Errorf("%s: Validate() = %v, want valid %t", name, err, c.valid)
		}
	}
}

func TestApplyReportTransitions(t *testing.T) {
	sentAt := time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)
	later := sentAt.Add(time.Minute)
	reason := "E_METER_LOCKED"
	command := func(status MeterCommandStatus) MeterCommand {
		c := MeterCommand{CommandID: reportCommandID, Status: status}
		switch status {
		case CommandStatusSent:
			c.SentAt = &sentAt
		case CommandStatusAcknowledged:
			c.SentAt, c.AcknowledgedAt = &sentAt, &sentAt
		case CommandStatusFailed:
			c.FailureReason = &reason
		}
		return c
	}
	report := func(status MeterCommandStatus) CommandReport {
		r := CommandReport{CommandID: reportCommandID, Status: status, OccurredAt: later}
		if status == CommandStatusFailed {
			r.FailureReason = reason
		}
		return r
	}

	type outcome int
	const (
		change outcome = iota
		repeat
		conflict
	)
	for _, c := range []struct {
		from, to MeterCommandStatus
		want     outcome
	}{
		{CommandStatusRequested, CommandStatusSent, change},
		{CommandStatusQueued, CommandStatusAcknowledged, change},
		{CommandStatusQueued, CommandStatusExpired, change},
		{CommandStatusSent, CommandStatusSent, repeat},
		{CommandStatusSent, CommandStatusAcknowledged, change},
		{CommandStatusSent, CommandStatusFailed, change},
		{CommandStatusAcknowledged, CommandStatusAcknowledged, repeat},
		{CommandStatusAcknowledged, CommandStatusSent, repeat},
		{CommandStatusAcknowledged, CommandStatusFailed, conflict},
		{CommandStatusAcknowledged, CommandStatusExpired, conflict},
		{CommandStatusFailed, CommandStatusFailed, repeat},
		{CommandStatusFailed, CommandStatusAcknowledged, conflict},
		{CommandStatusExpired, CommandStatusExpired, repeat},
		{CommandStatusExpired, CommandStatusSent, change},
		{CommandStatusExpired, CommandStatusAcknowledged, change},
	} {
		next, changed, err := command(c.from).ApplyReport(report(c.to))
		switch c.want {
		case change:
			if err != nil || !changed || next.Status != c.to {
				t.Errorf("%s -> %s = %s changed %t err %v, want a change", c.from, c.to, next.Status, changed, err)
			}
		case repeat:
			if err != nil || changed || next.Status != c.from {
				t.Errorf("%s -> %s = %s changed %t err %v, want a no-op", c.from, c.to, next.Status, changed, err)
			}
		case conflict:
			if !errors.Is(err, ErrConflict) {
				t.Errorf("%s -> %s err = %v, want ErrConflict", c.from, c.to, err)
			}
		}
	}
}

func TestApplyReportKeepsAcknowledgementAfterSend(t *testing.T) {
	sentAt := time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)
	cmd := MeterCommand{CommandID: reportCommandID, Status: CommandStatusSent, SentAt: &sentAt}
	next, _, err := cmd.ApplyReport(CommandReport{CommandID: reportCommandID, Status: CommandStatusAcknowledged, OccurredAt: sentAt.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if next.AcknowledgedAt == nil || next.AcknowledgedAt.Before(*next.SentAt) {
		t.Fatalf("acknowledged at %v before sent at %v", next.AcknowledgedAt, next.SentAt)
	}

	fresh := MeterCommand{CommandID: reportCommandID, Status: CommandStatusQueued}
	next, _, err = fresh.ApplyReport(CommandReport{CommandID: reportCommandID, Status: CommandStatusAcknowledged, OccurredAt: sentAt})
	if err != nil || next.SentAt == nil || !next.SentAt.Equal(sentAt) {
		t.Fatalf("acknowledging an unsent command = %+v, %v; want sent_at filled in", next, err)
	}
}

func TestClampOccurredAtBoundsGatewayClock(t *testing.T) {
	requested := time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)
	now := requested.Add(time.Hour)
	cmd := MeterCommand{RequestedAt: requested}
	for name, c := range map[string]struct{ at, want time.Time }{
		"gateway behind": {requested.Add(-24 * time.Hour), requested},
		"gateway ahead":  {now.Add(24 * time.Hour), now},
		"plausible":      {requested.Add(time.Minute), requested.Add(time.Minute)},
	} {
		if got := cmd.ClampOccurredAt(c.at, now); !got.Equal(c.want) {
			t.Errorf("%s: clamp(%s) = %s, want %s", name, c.at, got, c.want)
		}
	}
}
