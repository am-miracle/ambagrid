package health

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
)

type collector struct {
	source         StatsSource
	depth          *prometheus.Desc
	payloadBytes   *prometheus.Desc
	capacityBytes  *prometheus.Desc
	diskBytes      *prometheus.Desc
	filesystemFree *prometheus.Desc
	oldestSeconds  *prometheus.Desc
	state          *prometheus.Desc
	statsAvailable *prometheus.Desc
}

func newCollector(source StatsSource) *collector {
	return &collector{
		source:         source,
		depth:          prometheus.NewDesc("ambagrid_edge_queue_depth", "Number of events waiting for upload.", nil, nil),
		payloadBytes:   prometheus.NewDesc("ambagrid_edge_queue_payload_bytes", "Payload bytes currently held in the durable queue.", nil, nil),
		capacityBytes:  prometheus.NewDesc("ambagrid_edge_queue_capacity_bytes", "Payload capacity available within the configured storage budget.", nil, nil),
		diskBytes:      prometheus.NewDesc("ambagrid_edge_queue_disk_bytes", "Bytes occupied by the SQLite database, WAL, and shared-memory files.", nil, nil),
		filesystemFree: prometheus.NewDesc("ambagrid_edge_filesystem_free_bytes", "Bytes available on the filesystem containing the queue.", nil, nil),
		oldestSeconds:  prometheus.NewDesc("ambagrid_edge_queue_oldest_seconds", "Age in seconds of the oldest pending event.", nil, nil),
		state:          prometheus.NewDesc("ambagrid_edge_storage_state", "Current queue admission and storage state.", []string{"state"}, nil),
		statsAvailable: prometheus.NewDesc("ambagrid_edge_queue_stats_available", "Whether queue statistics could be read for this scrape.", nil, nil),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.depth
	ch <- c.payloadBytes
	ch <- c.capacityBytes
	ch <- c.diskBytes
	ch <- c.filesystemFree
	ch <- c.oldestSeconds
	ch <- c.state
	ch <- c.statsAvailable
}

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
	ch <- prometheus.MustNewConstMetric(c.payloadBytes, prometheus.GaugeValue, float64(stats.PayloadBytes))
	ch <- prometheus.MustNewConstMetric(c.capacityBytes, prometheus.GaugeValue, float64(stats.CapacityBytes))
	ch <- prometheus.MustNewConstMetric(c.diskBytes, prometheus.GaugeValue, float64(stats.DiskBytes))
	ch <- prometheus.MustNewConstMetric(c.filesystemFree, prometheus.GaugeValue, float64(stats.FilesystemFreeBytes))
	ch <- prometheus.MustNewConstMetric(c.oldestSeconds, prometheus.GaugeValue, stats.OldestAge.Seconds())
	ch <- prometheus.MustNewConstMetric(c.state, prometheus.GaugeValue, 1, string(stats.State))
}
