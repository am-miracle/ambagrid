package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"api-go/internal/domain"
)

func TestMeterCommandOutcomePayloadMatchesSharedFixtures(t *testing.T) {
	credit := 20.0
	reason := "superseded by newer relay command cmd-0104"
	for fixture, c := range map[string]struct {
		cmd        domain.MeterCommand
		occurredAt time.Time
	}{
		"meter_command_acknowledged_outbox_payload.json": {
			cmd: domain.MeterCommand{
				CommandID: "cmd-0102", MeterID: "met-0101", CommandType: domain.CommandCreditMeter,
				Status: domain.CommandStatusAcknowledged, CreditKWh: &credit,
			},
			occurredAt: time.Date(2026, 10, 1, 14, 31, 0, 0, time.UTC),
		},
		"meter_command_failed_outbox_payload.json": {
			cmd: domain.MeterCommand{
				CommandID: "cmd-0103", MeterID: "met-0101", CommandType: domain.CommandDisconnectMeter,
				Status: domain.CommandStatusFailed, FailureReason: &reason,
			},
			occurredAt: time.Date(2026, 10, 1, 14, 32, 0, 0, time.UTC),
		},
	} {
		payload, err := meterCommandOutcomePayload(c.cmd, "site-0101", "edge:site-0101", c.occurredAt)
		if err != nil {
			t.Fatal(err)
		}
		fixtureBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "proto", "fixtures", fixture))
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		var got, want map[string]any
		if err := json.Unmarshal(payload, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(fixtureBytes, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: payload = %#v, want %#v", fixture, got, want)
		}
	}
}
