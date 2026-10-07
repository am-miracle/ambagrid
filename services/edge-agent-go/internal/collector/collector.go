// Package collector reads hardware adapters and persists normalized telemetry.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type Config struct {
	SiteID   string
	Region   string
	Interval time.Duration
}

type Collector struct {
	cfg     Config
	queue   Queue
	sources []Source
	now     func() time.Time
}

func New(cfg Config, queue Queue, sources ...Source) (*Collector, error) {
	if strings.TrimSpace(cfg.SiteID) == "" || strings.TrimSpace(cfg.Region) == "" {
		return nil, errors.New("collector site ID and region are required")
	}
	if cfg.Interval <= 0 {
		return nil, errors.New("collector interval must be positive")
	}
	if queue == nil {
		return nil, errors.New("collector queue is required")
	}
	if len(sources) == 0 {
		return nil, errors.New("at least one telemetry source is required")
	}
	return &Collector{cfg: cfg, queue: queue, sources: sources, now: time.Now}, nil
}

func (c *Collector) Run(ctx context.Context) {
	c.collectAndReport(ctx)
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collectAndReport(ctx)
		}
	}
}

func (c *Collector) collectAndReport(ctx context.Context) {
	if err := c.CollectOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("collect edge telemetry", "error", err)
	}
}

func (c *Collector) CollectOnce(ctx context.Context) error {
	var collectionErr error
	for _, source := range c.sources {
		readings, err := source.Read(ctx)
		if err != nil {
			collectionErr = errors.Join(collectionErr, fmt.Errorf("read telemetry source: %w", err))
			continue
		}
		for _, reading := range readings {
			event, err := normalize(c.cfg.SiteID, c.cfg.Region, reading, c.now().UTC())
			if err != nil {
				collectionErr = errors.Join(collectionErr, fmt.Errorf("normalize device %q: %w", reading.DeviceID, err))
				continue
			}
			sequence, err := c.queue.Persist(ctx, event)
			if err != nil {
				collectionErr = errors.Join(collectionErr, fmt.Errorf("persist device %q: %w", reading.DeviceID, err))
				continue
			}
			if err := c.queue.MarkPendingUpload(ctx, sequence); err != nil {
				collectionErr = errors.Join(collectionErr, fmt.Errorf("queue device %q sequence %d for upload: %w", reading.DeviceID, sequence, err))
			}
		}
	}
	return collectionErr
}
