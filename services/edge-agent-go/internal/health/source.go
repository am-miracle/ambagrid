package health

import (
	"context"
	"time"

	"edge-agent-go/internal/domain"
)

// StatsSource is the queue telemetry required by the health transport.
// The consumer owns this interface so storage implementations remain replaceable.
type StatsSource interface {
	Stats(context.Context) (domain.QueueStats, error)
}

type FallbackStatsSource interface {
	FallbackRateLimitStats(context.Context, time.Time, domain.FallbackRateLimitPolicy) (domain.FallbackRateLimitStats, error)
}
