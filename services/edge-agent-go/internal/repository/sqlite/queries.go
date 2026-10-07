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

func (s *Store) Ready(ctx context.Context, limit int, maxBytes int64) ([]domain.Record, error) {
	if limit < 1 || maxBytes < 1 {
		return nil, errors.New("ready limit and max bytes must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT sequence, site_id, gateway_id, device_id, asset_type, mqtt_topic, payload, priority, status,
       event_at_ms, received_at_ms, persisted_at_ms, attempt_count,
       next_attempt_at_ms, COALESCE(last_error, '')
FROM queue_events
WHERE status = ?
  AND (next_attempt_at_ms IS NULL OR next_attempt_at_ms <= ?)
  AND NOT EXISTS (
      SELECT 1
      FROM queue_events AS earlier
      WHERE earlier.device_id = queue_events.device_id
        AND earlier.sequence < queue_events.sequence
        AND earlier.status IN ('persisted', 'pending_upload', 'uploaded')
  )
ORDER BY sequence
LIMIT ?`, domain.StatusPendingUpload, time.Now().UTC().UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("query ready events: %w", err)
	}
	defer rows.Close()

	records := make([]domain.Record, 0, limit)
	var totalBytes int64
	for rows.Next() {
		var record domain.Record
		var eventMS, receivedMS, persistedMS int64
		var nextAttemptMS sql.NullInt64
		if err := rows.Scan(
			&record.Sequence, &record.SiteID, &record.GatewayID, &record.DeviceID, &record.AssetType,
			&record.MQTTTopic, &record.Payload, &record.Priority, &record.Status, &eventMS,
			&receivedMS, &persistedMS, &record.AttemptCount, &nextAttemptMS,
			&record.LastError,
		); err != nil {
			return nil, fmt.Errorf("scan ready event: %w", err)
		}
		if len(records) > 0 && totalBytes+int64(len(record.Payload)) > maxBytes {
			break
		}
		record.EventTimestamp = time.UnixMilli(eventMS).UTC()
		record.EdgeReceivedAt = time.UnixMilli(receivedMS).UTC()
		record.PersistedAt = time.UnixMilli(persistedMS).UTC()
		if nextAttemptMS.Valid {
			next := time.UnixMilli(nextAttemptMS.Int64).UTC()
			record.NextAttemptAt = &next
		}
		records = append(records, record)
		totalBytes += int64(len(record.Payload))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ready events: %w", err)
	}
	return records, nil
}

func (s *Store) Record(ctx context.Context, sequence uint64) (domain.Record, error) {
	var record domain.Record
	var eventMS, receivedMS, persistedMS int64
	var nextAttemptMS sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT sequence, site_id, gateway_id, device_id, asset_type, mqtt_topic, payload, priority, status,
       event_at_ms, received_at_ms, persisted_at_ms, attempt_count,
       next_attempt_at_ms, COALESCE(last_error, '')
FROM queue_events
WHERE sequence = ?`, sequence).Scan(
		&record.Sequence, &record.SiteID, &record.GatewayID, &record.DeviceID, &record.AssetType,
		&record.MQTTTopic, &record.Payload, &record.Priority, &record.Status, &eventMS,
		&receivedMS, &persistedMS, &record.AttemptCount, &nextAttemptMS, &record.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Record{}, fmt.Errorf("%w: sequence %d", domain.ErrEventNotFound, sequence)
	}
	if err != nil {
		return domain.Record{}, fmt.Errorf("read queue event %d: %w", sequence, err)
	}
	record.EventTimestamp = time.UnixMilli(eventMS).UTC()
	record.EdgeReceivedAt = time.UnixMilli(receivedMS).UTC()
	record.PersistedAt = time.UnixMilli(persistedMS).UTC()
	if nextAttemptMS.Valid {
		next := time.UnixMilli(nextAttemptMS.Int64).UTC()
		record.NextAttemptAt = &next
	}
	return record, nil
}

func (s *Store) Stats(ctx context.Context) (domain.QueueStats, error) {
	stats := domain.QueueStats{
		CapacityBytes:        s.payloadCapacity(),
		NormalAdmissionBytes: s.normalAdmissionLimit(),
	}
	var oldestMS sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(payload_bytes), 0), MIN(received_at_ms)
FROM queue_events
WHERE status IN ('persisted', 'pending_upload', 'uploaded')`).Scan(&stats.Depth, &stats.PayloadBytes, &oldestMS); err != nil {
		return domain.QueueStats{}, fmt.Errorf("read queue statistics: %w", err)
	}
	if oldestMS.Valid {
		stats.OldestAge = time.Since(time.UnixMilli(oldestMS.Int64))
		if stats.OldestAge < 0 {
			stats.OldestAge = 0
		}
	}
	diskBytes, err := queueDiskBytes(s.cfg.Path)
	if err != nil {
		return domain.QueueStats{}, fmt.Errorf("measure queue disk usage: %w", err)
	}
	stats.DiskBytes = diskBytes
	freeBytes, err := filesystemFreeBytes(filepath.Dir(s.cfg.Path))
	if err != nil {
		return domain.QueueStats{}, fmt.Errorf("measure filesystem capacity: %w", err)
	}
	stats.FilesystemFreeBytes = freeBytes
	stats.State = s.queueState(stats)
	return stats, nil
}

func (s *Store) queueState(stats domain.QueueStats) domain.QueueState {
	switch {
	case s.cfg.MinFilesystemFreeBytes > 0 && stats.FilesystemFreeBytes < s.cfg.MinFilesystemFreeBytes:
		return domain.QueueFilesystemLow
	case stats.DiskBytes >= s.cfg.MaxStorageBytes || stats.PayloadBytes >= stats.CapacityBytes:
		return domain.QueueFull
	case stats.PayloadBytes >= s.normalAdmissionLimit():
		return domain.QueueNormalSuspended
	case stats.DiskBytes*100 >= s.cfg.MaxStorageBytes*int64(s.cfg.WarningPercent) ||
		stats.PayloadBytes*100 >= stats.CapacityBytes*int64(s.cfg.WarningPercent):
		return domain.QueueWarning
	default:
		return domain.QueueHealthy
	}
}
