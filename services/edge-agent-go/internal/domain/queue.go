// domain defines the edge agent's durable queue vocabulary.
package domain

import (
	"context"
	"time"
)

type QueueState string

const (
	QueueHealthy         QueueState = "healthy"
	QueueWarning         QueueState = "warning"
	QueueNormalSuspended QueueState = "normal_suspended"
	QueueFull            QueueState = "full"
	QueueFilesystemLow   QueueState = "filesystem_low"
)

type QueueStats struct {
	Depth                int64
	PayloadBytes         int64
	CapacityBytes        int64
	NormalAdmissionBytes int64
	DiskBytes            int64
	FilesystemFreeBytes  int64
	OldestAge            time.Duration
	OldestPendingAt      *time.Time
	LastEventTimestamp   *time.Time
	State                QueueState
}

// Queue is the durable store-and-forward lifecycle shared by the collector
// (write side) and the uploader (read side). Both depend on this interface
// rather than defining their own, so the SQLite store satisfies one contract.
type Queue interface {
	Persist(ctx context.Context, event Event) (uint64, error)
	MarkPendingUpload(ctx context.Context, sequence uint64) error
	Ready(ctx context.Context, limit int, maxBytes int64) ([]Record, error)
	MarkUploaded(ctx context.Context, sequence uint64, uploadedAt time.Time) error
	Ack(ctx context.Context, sequence uint64) error
	MarkFailed(ctx context.Context, sequence uint64, retryAt time.Time, cause string) error
	Stats(ctx context.Context) (QueueStats, error)
}
