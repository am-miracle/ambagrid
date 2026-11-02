package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"api-go/internal/domain"
)

type commandEvidenceReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readLatestCommandConflict(ctx context.Context, reader commandEvidenceReader, commandID string) (*domain.RetainedCommandReport, error) {
	var conflict domain.RetainedCommandReport
	var raw []byte
	err := reader.QueryRow(ctx, `SELECT audit_event_id::text,metadata,occurred_at,count(*) OVER() FROM audit_events
  WHERE subject_type='meter_command' AND subject_id=$1 AND action='meter_command.report_conflict'
  ORDER BY occurred_at DESC,audit_event_id DESC LIMIT 1`, commandID).Scan(&conflict.ReportID, &raw, &conflict.ReceivedAt, &conflict.ConflictCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read conflicting command evidence: %w", err)
	}
	report, err := decodeConflictMetadata(raw)
	if err != nil {
		return nil, err
	}
	conflict.Status, conflict.OccurredAt, conflict.FailureReason, conflict.Observation = report.Status, report.OccurredAt, report.FailureReason, report.Observation
	return &conflict, nil
}

type conflictMetadata struct {
	Status        domain.MeterCommandStatus  `json:"reported_status"`
	OccurredAt    time.Time                  `json:"occurred_at"`
	FailureReason string                     `json:"failure_reason"`
	Observation   *domain.CommandObservation `json:"observation"`
}

func decodeConflictMetadata(raw []byte) (conflictMetadata, error) {
	var report conflictMetadata
	if err := json.Unmarshal(raw, &report); err != nil {
		return report, fmt.Errorf("decode conflicting command evidence: %w", err)
	}
	return report, nil
}

const commandObservationSQL = `SELECT observed_at,received_at,relay_closed,balance_kwh,tamper_detected,coalesce(firmware_version,''),coalesce(fault_code,'')
  FROM meter_command_observations WHERE command_id=$1::uuid`

// readCommandObservation returns a nil observation when the device reported none.
func readCommandObservation(ctx context.Context, reader commandEvidenceReader, commandID string) (observation *domain.CommandObservation, observedAt, receivedAt *time.Time, err error) {
	var o domain.CommandObservation
	err = reader.QueryRow(ctx, commandObservationSQL, commandID).Scan(&observedAt, &receivedAt, &o.RelayClosed, &o.BalanceKWh, &o.TamperDetected, &o.FirmwareVersion, &o.FaultCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read command observation: %w", err)
	}
	return &o, observedAt, receivedAt, nil
}
