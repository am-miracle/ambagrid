package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"edge-agent-go/internal/domain"
)

const (
	maxDeviceIDBytes     = 256
	maxMQTTTopicBytes    = 1024
	maxFailureCauseBytes = 2048
)

func (s *Store) Enqueue(ctx context.Context, event domain.Event) (uint64, error) {
	return s.insert(ctx, event, domain.StatusPendingUpload)
}

// Persist durably records an event before it becomes eligible for upload.
func (s *Store) Persist(ctx context.Context, event domain.Event) (uint64, error) {
	return s.insert(ctx, event, domain.StatusPersisted)
}

func (s *Store) insert(ctx context.Context, event domain.Event, status domain.EventStatus) (uint64, error) {
	if event.DeviceID == "" || event.MQTTTopic == "" || len(event.Payload) == 0 {
		return 0, errors.New("device ID, MQTT topic, and payload are required")
	}
	if !event.AssetType.Valid() {
		return 0, errors.New("event asset type is invalid")
	}
	if event.EventTimestamp.IsZero() || event.EdgeReceivedAt.IsZero() {
		return 0, errors.New("event and received timestamps are required")
	}
	if len(event.DeviceID) > maxDeviceIDBytes || len(event.MQTTTopic) > maxMQTTTopicBytes {
		return 0, errors.New("event identity metadata is too large")
	}
	if !event.Priority.Valid() {
		return 0, errors.New("event priority is invalid")
	}
	if int64(len(event.Payload)) > s.cfg.MaxEventBytes {
		return 0, domain.ErrEventTooLarge
	}
	if err := s.checkAdmission(event); err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin queue admission: %w", err)
	}
	defer tx.Rollback()

	var queuedBytes int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(payload_bytes), 0) FROM queue_events WHERE status IN ('persisted', 'pending_upload', 'uploaded')").Scan(&queuedBytes); err != nil {
		return 0, fmt.Errorf("measure queue capacity: %w", err)
	}
	payloadBytes := int64(len(event.Payload))
	if payloadBytes > s.payloadCapacity()-queuedBytes {
		return 0, domain.ErrStorageCapacity
	}
	if event.Priority == domain.PriorityNormal && payloadBytes > s.normalAdmissionLimit()-queuedBytes {
		return 0, domain.ErrCriticalReserve
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO queue_events(
	    site_id, gateway_id, device_id, asset_type, mqtt_topic, payload, payload_bytes,
	    priority, event_at_ms, received_at_ms, persisted_at_ms, status, critical_code, critical_value
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.cfg.SiteID, s.cfg.GatewayID, event.DeviceID, event.AssetType, event.MQTTTopic,
		event.Payload, len(event.Payload), event.Priority,
		event.EventTimestamp.UnixMilli(), event.EdgeReceivedAt.UnixMilli(), time.Now().UTC().UnixMilli(), status,
		nullString(event.CriticalCode), event.CriticalValue,
	)
	if err != nil {
		if isSQLiteFull(err) {
			return 0, domain.ErrStorageCapacity
		}
		return 0, fmt.Errorf("persist queue event: %w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read queue sequence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit queue event: %w", err)
	}
	return uint64(sequence), nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) MarkPendingUpload(ctx context.Context, sequence uint64) error {
	return s.transition(ctx, sequence, domain.StatusPersisted, domain.StatusPendingUpload)
}

// MarkUploaded uses a dedicated UPDATE instead of the generic transition()
// because it also stamps uploaded_at_ms in the same write.
func (s *Store) MarkUploaded(ctx context.Context, sequence uint64, uploadedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE queue_events
SET status = ?, uploaded_at_ms = ?, next_attempt_at_ms = NULL, last_error = NULL
WHERE sequence = ? AND status = ?`, domain.StatusUploaded, uploadedAt.UTC().UnixMilli(), sequence, domain.StatusPendingUpload)
	if err != nil {
		return fmt.Errorf("mark queue event %d uploaded: %w", sequence, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read upload mark result: %w", err)
	}
	if updated > 0 {
		return nil
	}
	var current domain.EventStatus
	if err := s.db.QueryRowContext(ctx, "SELECT status FROM queue_events WHERE sequence = ?", sequence).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: sequence %d", domain.ErrEventNotFound, sequence)
		}
		return fmt.Errorf("read queue event %d status: %w", sequence, err)
	}
	if current == domain.StatusUploaded {
		return nil
	}
	return fmt.Errorf("%w: sequence %d is %s, expected %s before %s", domain.ErrInvalidTransition, sequence, current, domain.StatusPendingUpload, domain.StatusUploaded)
}

func (s *Store) Ack(ctx context.Context, sequence uint64) error {
	return s.transition(ctx, sequence, domain.StatusUploaded, domain.StatusAcknowledged)
}

func (s *Store) Expire(ctx context.Context, sequence uint64) error {
	return s.transition(ctx, sequence, domain.StatusAcknowledged, domain.StatusExpired)
}

func (s *Store) transition(ctx context.Context, sequence uint64, from, to domain.EventStatus) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE queue_events
SET status = ?, next_attempt_at_ms = NULL, last_error = NULL
WHERE sequence = ? AND status = ?`, to, sequence, from)
	if err != nil {
		return fmt.Errorf("transition queue event %d from %s to %s: %w", sequence, from, to, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read queue event %d transition result: %w", sequence, err)
	}
	if updated > 0 {
		return nil
	}

	var current domain.EventStatus
	if err := s.db.QueryRowContext(ctx, "SELECT status FROM queue_events WHERE sequence = ?", sequence).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: sequence %d", domain.ErrEventNotFound, sequence)
		}
		return fmt.Errorf("read queue event %d status: %w", sequence, err)
	}
	if current == to {
		return nil
	}
	return fmt.Errorf("%w: sequence %d is %s, expected %s before %s", domain.ErrInvalidTransition, sequence, current, from, to)
}

func (s *Store) recoverPersisted(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE queue_events SET status = ? WHERE status = ?", domain.StatusPendingUpload, domain.StatusPersisted); err != nil {
		return fmt.Errorf("recover persisted queue events: %w", err)
	}
	return nil
}

func (s *Store) checkAdmission(event domain.Event) error {
	writeHeadroom := s.writeHeadroom(event)
	freeBytes, err := filesystemFreeBytes(filepath.Dir(s.cfg.Path))
	if err != nil {
		return fmt.Errorf("measure filesystem capacity: %w", err)
	}
	if s.cfg.MinFilesystemFreeBytes > 0 &&
		(freeBytes < s.cfg.MinFilesystemFreeBytes || writeHeadroom > freeBytes-s.cfg.MinFilesystemFreeBytes) {
		return domain.ErrFilesystemReserve
	}
	diskBytes, err := queueDiskBytes(s.cfg.Path)
	if err != nil {
		return fmt.Errorf("measure queue disk usage: %w", err)
	}
	if diskBytes >= s.cfg.MaxStorageBytes || writeHeadroom > s.cfg.MaxStorageBytes-diskBytes {
		return domain.ErrStorageCapacity
	}
	return nil
}

func (s *Store) MarkFailed(ctx context.Context, sequence uint64, retryAt time.Time, cause string) error {
	if retryAt.IsZero() {
		return errors.New("retry timestamp is required")
	}
	if cause == "" {
		return errors.New("failure cause is required")
	}
	if len(cause) > maxFailureCauseBytes {
		return errors.New("failure cause is too large")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE queue_events
SET status = ?,
    attempt_count = attempt_count + 1,
    next_attempt_at_ms = ?,
    last_error = ?
WHERE sequence = ? AND status IN (?, ?)`, domain.StatusPendingUpload, retryAt.UTC().UnixMilli(), cause, sequence, domain.StatusPendingUpload, domain.StatusUploaded)
	if err != nil {
		return fmt.Errorf("mark queue event %d failed: %w", sequence, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read failed event update result: %w", err)
	}
	if updated == 0 {
		return fmt.Errorf("%w: sequence %d is not pending or uploaded", domain.ErrInvalidTransition, sequence)
	}
	return nil
}
