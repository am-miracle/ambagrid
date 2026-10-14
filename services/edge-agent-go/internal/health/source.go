package health

import (
	"context"

	"edge-agent-go/internal/domain"
)

// StatsSource is the queue telemetry required by the health transport.
// The consumer owns this interface so storage implementations remain replaceable.
type StatsSource interface {
	Stats(context.Context) (domain.QueueStats, error)
}
