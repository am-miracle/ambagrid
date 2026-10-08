// collector reads hardware adapters and persists normalized telemetry.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"edge-agent-go/internal/domain"
	"edge-agent-go/internal/fallback"
)

type Config struct {
	SiteID           string
	Region           string
	Interval         time.Duration
	BatteryOverheatC float64
}

type FallbackStore interface {
	OpenFallbackIncident(context.Context, fallback.Event, time.Time) error
	ResolveFallbackIncident(context.Context, string, fallback.Code, time.Time) error
}

type Collector struct {
	cfg      Config
	queue    domain.Queue
	sources  []Source
	now      func() time.Time
	fallback FallbackStore
}

func New(cfg Config, queue domain.Queue, sources ...Source) (*Collector, error) {
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
	if cfg.BatteryOverheatC <= 0 {
		cfg.BatteryOverheatC = 55
	}
	c := &Collector{cfg: cfg, queue: queue, sources: sources, now: time.Now}
	if store, ok := queue.(FallbackStore); ok {
		c.fallback = store
	}
	return c, nil
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
			now := c.now().UTC()
			event, err := normalize(c.cfg.SiteID, c.cfg.Region, reading, now)
			if err != nil {
				collectionErr = errors.Join(collectionErr, fmt.Errorf("normalize device %q: %w", reading.DeviceID, err))
				continue
			}
			criticalEvents := c.criticalEvents(reading)
			if len(criticalEvents) == 0 {
				collectionErr = errors.Join(collectionErr, c.persist(ctx, event, nil, now))
			} else {
				for i := range criticalEvents {
					criticalEvent := criticalEvents[i]
					criticalRecord := event
					criticalRecord.Priority = domain.PriorityCritical
					criticalRecord.CriticalCode = string(criticalEvent.Code)
					criticalRecord.CriticalValue = criticalEvent.TemperatureC
					collectionErr = errors.Join(collectionErr, c.persist(ctx, criticalRecord, &criticalEvent, now))
				}
			}
			collectionErr = errors.Join(collectionErr, c.resolveRecovered(ctx, reading, now))
		}
	}
	return collectionErr
}

func (c *Collector) persist(ctx context.Context, event domain.Event, critical *fallback.Event, now time.Time) error {
	sequence, err := c.queue.Persist(ctx, event)
	if err != nil {
		return fmt.Errorf("persist device %q: %w", event.DeviceID, err)
	}
	if critical != nil && c.fallback != nil {
		critical.Sequence = sequence
		if err := c.fallback.OpenFallbackIncident(ctx, *critical, now); err != nil {
			return fmt.Errorf("open critical incident for %q: %w", event.DeviceID, err)
		}
	}
	if err := c.queue.MarkPendingUpload(ctx, sequence); err != nil {
		return fmt.Errorf("queue device %q sequence %d for upload: %w", event.DeviceID, sequence, err)
	}
	return nil
}

func (c *Collector) criticalEvents(reading domain.Reading) []fallback.Event {
	base := fallback.Event{SiteID: c.cfg.SiteID, AssetID: reading.DeviceID, OccurredAt: reading.TakenAt.UTC()}
	var events []fallback.Event
	if reading.AssetType == domain.AssetBatteryBMS && reading.InternalTemperature >= c.cfg.BatteryOverheatC {
		event := base
		event.Code = fallback.BatteryOverheat
		temperature := reading.InternalTemperature
		event.TemperatureC = &temperature
		events = append(events, event)
	}
	if reading.AssetType == domain.AssetSolarInverter && reading.InverterFailed {
		event := base
		event.Code = fallback.InverterFailure
		events = append(events, event)
	}
	if reading.TamperDetected {
		event := base
		event.Code = fallback.TamperDetected
		events = append(events, event)
	}
	if reading.SiteOutage != nil && *reading.SiteOutage {
		event := base
		event.AssetID = "site"
		event.Code = fallback.SiteOutage
		events = append(events, event)
	}
	return events
}

func (c *Collector) resolveRecovered(ctx context.Context, reading domain.Reading, now time.Time) error {
	if c.fallback == nil {
		return nil
	}
	var result error
	if reading.AssetType == domain.AssetBatteryBMS && reading.InternalTemperature < c.cfg.BatteryOverheatC {
		result = errors.Join(result, c.fallback.ResolveFallbackIncident(ctx, reading.DeviceID, fallback.BatteryOverheat, now))
	}
	if reading.AssetType == domain.AssetSolarInverter && !reading.InverterFailed {
		result = errors.Join(result, c.fallback.ResolveFallbackIncident(ctx, reading.DeviceID, fallback.InverterFailure, now))
	}
	if !reading.TamperDetected {
		result = errors.Join(result, c.fallback.ResolveFallbackIncident(ctx, reading.DeviceID, fallback.TamperDetected, now))
	}
	if reading.SiteOutage != nil && !*reading.SiteOutage {
		result = errors.Join(result, c.fallback.ResolveFallbackIncident(ctx, "site", fallback.SiteOutage, now))
	}
	return result
}
