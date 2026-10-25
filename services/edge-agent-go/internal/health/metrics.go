package health

import (
	"context"
	"time"

	"edge-agent-go/internal/domain"

	"github.com/prometheus/client_golang/prometheus"
)

type collector struct {
	source                 StatsSource
	fallbackSource         FallbackStatsSource
	fallbackPolicy         domain.FallbackRateLimitPolicy
	depth                  *prometheus.Desc
	abandoned              *prometheus.Desc
	payloadBytes           *prometheus.Desc
	capacityBytes          *prometheus.Desc
	diskBytes              *prometheus.Desc
	filesystemFree         *prometheus.Desc
	oldestSeconds          *prometheus.Desc
	state                  *prometheus.Desc
	statsAvailable         *prometheus.Desc
	fallbackRateLimited    *prometheus.Desc
	fallbackStatsAvailable *prometheus.Desc
	commandSource          CommandStatsSource
	commandsUnresolved     *prometheus.Desc
	commandsOldest         *prometheus.Desc
	commandStatsAvailable  *prometheus.Desc
	reportsPending         *prometheus.Desc
	reportsAbandoned       *prometheus.Desc
}

// newCollector builds one Prometheus collector for queue health and, when the
// source supports it, durable SMS fallback rate-limit state.
func newCollector(source StatsSource, fallbackPolicy domain.FallbackRateLimitPolicy) *collector {
	c := &collector{
		source:                 source,
		fallbackPolicy:         fallbackPolicy,
		depth:                  prometheus.NewDesc("ambagrid_edge_queue_depth", "Number of events waiting for upload.", nil, nil),
		abandoned:              prometheus.NewDesc("ambagrid_edge_queue_abandoned_total", "Total number of undelivered events abandoned after terminal server rejections.", nil, nil),
		payloadBytes:           prometheus.NewDesc("ambagrid_edge_queue_payload_bytes", "Payload bytes currently held in the durable queue.", nil, nil),
		capacityBytes:          prometheus.NewDesc("ambagrid_edge_queue_capacity_bytes", "Payload capacity available within the configured storage budget.", nil, nil),
		diskBytes:              prometheus.NewDesc("ambagrid_edge_queue_disk_bytes", "Bytes occupied by the SQLite database, WAL, and shared-memory files.", nil, nil),
		filesystemFree:         prometheus.NewDesc("ambagrid_edge_filesystem_free_bytes", "Bytes available on the filesystem containing the queue.", nil, nil),
		oldestSeconds:          prometheus.NewDesc("ambagrid_edge_queue_oldest_seconds", "Age in seconds of the oldest pending event.", nil, nil),
		state:                  prometheus.NewDesc("ambagrid_edge_storage_state", "Current queue admission and storage state.", []string{"state"}, nil),
		statsAvailable:         prometheus.NewDesc("ambagrid_edge_queue_stats_available", "Whether queue statistics could be read for this scrape.", nil, nil),
		fallbackRateLimited:    prometheus.NewDesc("ambagrid_edge_fallback_rate_limited_incidents", "Active fallback incidents currently blocked by an SMS attempt limit.", []string{"reason"}, nil),
		fallbackStatsAvailable: prometheus.NewDesc("ambagrid_edge_fallback_stats_available", "Whether fallback rate-limit statistics could be read for this scrape.", nil, nil),
		commandsUnresolved:     prometheus.NewDesc("ambagrid_edge_commands_unresolved", "Meter commands sent to a device whose outcome has not been confirmed.", nil, nil),
		commandsOldest:         prometheus.NewDesc("ambagrid_edge_commands_oldest_unresolved_seconds", "Age in seconds of the oldest unresolved meter command.", nil, nil),
		commandStatsAvailable:  prometheus.NewDesc("ambagrid_edge_command_stats_available", "Whether command ledger statistics could be read for this scrape.", nil, nil),
		reportsPending:         prometheus.NewDesc("ambagrid_edge_command_reports_pending", "Command outcome reports not yet accepted by the platform.", nil, nil),
		reportsAbandoned:       prometheus.NewDesc("ambagrid_edge_command_reports_abandoned", "Command outcome reports the platform refused and that will not be retried.", nil, nil),
	}
	// Queue implementations that do not persist fallback state still expose
	// queue metrics; fallback_stats_available reports that reduced capability.
	if fallbackSource, ok := source.(FallbackStatsSource); ok {
		c.fallbackSource = fallbackSource
	}
	if commandSource, ok := source.(CommandStatsSource); ok {
		c.commandSource = commandSource
	}
	return c
}

// Describe publishes every descriptor this collector may emit.
func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.depth
	ch <- c.abandoned
	ch <- c.payloadBytes
	ch <- c.capacityBytes
	ch <- c.diskBytes
	ch <- c.filesystemFree
	ch <- c.oldestSeconds
	ch <- c.state
	ch <- c.statsAvailable
	ch <- c.fallbackRateLimited
	ch <- c.fallbackStatsAvailable
	ch <- c.commandsUnresolved
	ch <- c.commandsOldest
	ch <- c.commandStatsAvailable
	ch <- c.reportsPending
	ch <- c.reportsAbandoned
}

// Collect reads one bounded snapshot so a stalled local store cannot block a
// Prometheus scrape indefinitely.
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()

	stats, err := c.source.Stats(ctx)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.statsAvailable, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.statsAvailable, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.depth, prometheus.GaugeValue, float64(stats.Depth))
	ch <- prometheus.MustNewConstMetric(c.abandoned, prometheus.CounterValue, float64(stats.AbandonedTotal))
	ch <- prometheus.MustNewConstMetric(c.payloadBytes, prometheus.GaugeValue, float64(stats.PayloadBytes))
	ch <- prometheus.MustNewConstMetric(c.capacityBytes, prometheus.GaugeValue, float64(stats.CapacityBytes))
	ch <- prometheus.MustNewConstMetric(c.diskBytes, prometheus.GaugeValue, float64(stats.DiskBytes))
	ch <- prometheus.MustNewConstMetric(c.filesystemFree, prometheus.GaugeValue, float64(stats.FilesystemFreeBytes))
	ch <- prometheus.MustNewConstMetric(c.oldestSeconds, prometheus.GaugeValue, stats.OldestAge.Seconds())
	ch <- prometheus.MustNewConstMetric(c.state, prometheus.GaugeValue, 1, string(stats.State))
	c.collectCommands(ctx, ch)

	// Fallback collection is independent of queue collection. Its availability
	// metric distinguishes a healthy zero from an unavailable data source.
	if c.fallbackSource == nil {
		ch <- prometheus.MustNewConstMetric(c.fallbackStatsAvailable, prometheus.GaugeValue, 0)
		return
	}
	fallbackStats, err := c.fallbackSource.FallbackRateLimitStats(ctx, time.Now().UTC(), c.fallbackPolicy)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.fallbackStatsAvailable, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.fallbackStatsAvailable, prometheus.GaugeValue, 1)
	// A single labeled gauge keeps the three policy limits easy to aggregate
	// while preserving the reason each active incident is currently blocked.
	ch <- prometheus.MustNewConstMetric(c.fallbackRateLimited, prometheus.GaugeValue, float64(fallbackStats.IncidentAttemptLimit), "incident_attempt_limit")
	ch <- prometheus.MustNewConstMetric(c.fallbackRateLimited, prometheus.GaugeValue, float64(fallbackStats.SiteHourlyLimit), "site_hourly_limit")
	ch <- prometheus.MustNewConstMetric(c.fallbackRateLimited, prometheus.GaugeValue, float64(fallbackStats.SiteDailyLimit), "site_daily_limit")
}

func (c *collector) collectCommands(ctx context.Context, ch chan<- prometheus.Metric) {
	if c.commandSource == nil {
		ch <- prometheus.MustNewConstMetric(c.commandStatsAvailable, prometheus.GaugeValue, 0)
		return
	}
	stats, err := c.commandSource.CommandLedgerStats(ctx, time.Now().UTC())
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.commandStatsAvailable, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.commandStatsAvailable, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.commandsUnresolved, prometheus.GaugeValue, float64(stats.Unresolved))
	ch <- prometheus.MustNewConstMetric(c.commandsOldest, prometheus.GaugeValue, stats.OldestUnresolvedAge.Seconds())
	ch <- prometheus.MustNewConstMetric(c.reportsPending, prometheus.GaugeValue, float64(stats.PendingReports))
	ch <- prometheus.MustNewConstMetric(c.reportsAbandoned, prometheus.GaugeValue, float64(stats.AbandonedReports))
}
