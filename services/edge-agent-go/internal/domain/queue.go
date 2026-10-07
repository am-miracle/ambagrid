// Package domain defines the edge agent's durable queue vocabulary.
package domain

import "time"

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
	State                QueueState
}
