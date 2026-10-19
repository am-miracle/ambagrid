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
	FallbackDebounce time.Duration
}

type FallbackStore interface {
	OpenFallbackIncident(context.Context, fallback.Event, time.Time) (bool, error)
	ResolveFallbackIncident(context.Context, string, fallback.Code, time.Time) error
}

type conditionKey struct {
	assetID string
	code    fallback.Code
}

type conditionState struct {
	activeSince time.Time
	opened      bool
}

type Collector struct {
	cfg        Config
	queue      domain.Queue
	sources    []Source
	now        func() time.Time
	fallback   FallbackStore
	conditions map[conditionKey]conditionState
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
	if cfg.FallbackDebounce <= 0 {
		cfg.FallbackDebounce = 30 * time.Second
	}
	c := &Collector{cfg: cfg, queue: queue, sources: sources, now: time.Now, conditions: make(map[conditionKey]conditionState)}
	if store, ok := queue.(FallbackStore); ok {
		c.fallback = store
	}
	return c, nil
}

// SetClock makes condition debounce deterministic for tests and simulations.
func (c *Collector) SetClock(now func() time.Time) {
	c.now = now
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
			criticalEvents := c.criticalEvents(reading, now)
			if len(criticalEvents) == 0 {
				collectionErr = errors.Join(collectionErr, c.persist(ctx, event, nil, now))
			} else {
				for i := range criticalEvents {
					criticalEvent := criticalEvents[i]
					criticalRecord := event
					criticalRecord.Priority = domain.PriorityCritical
					criticalRecord.CriticalCode = string(criticalEvent.Code)
					criticalRecord.CriticalValue = criticalEvent.TemperatureC
					if err := c.persist(ctx, criticalRecord, &criticalEvent, now); err != nil {
						collectionErr = errors.Join(collectionErr, err)
					} else {
						c.markOpened(criticalEvent)
					}
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
		opened, err := c.fallback.OpenFallbackIncident(ctx, *critical, now)
		if err != nil {
			return fmt.Errorf("open critical incident for %q: %w", event.DeviceID, err)
		}
		if !opened {
			slog.Debug("reuse active critical incident", "device_id", event.DeviceID, "code", critical.Code)
		}
	}
	if err := c.queue.MarkPendingUpload(ctx, sequence); err != nil {
		return fmt.Errorf("queue device %q sequence %d for upload: %w", event.DeviceID, sequence, err)
	}
	return nil
}

func (c *Collector) criticalEvents(reading domain.Reading, now time.Time) []fallback.Event {
	base := fallback.Event{SiteID: c.cfg.SiteID, AssetID: reading.DeviceID, OccurredAt: reading.TakenAt.UTC()}
	var events []fallback.Event
	if reading.AssetType == domain.AssetBatteryBMS && reading.InternalTemperature >= c.cfg.BatteryOverheatC {
		event := base
		event.Code = fallback.BatteryOverheat
		temperature := reading.InternalTemperature
		event.TemperatureC = &temperature
		events = c.openOnce(events, event, now, 0)
	}
	if reading.AssetType == domain.AssetSolarInverter && (reading.InverterFailed || reading.InverterHardFault) {
		event := base
		event.Code = fallback.InverterFailure
		debounce := c.cfg.FallbackDebounce
		if reading.InverterHardFault {
			debounce = 0
		}
		events = c.openOnce(events, event, now, debounce)
	}
	if reading.TamperDetected {
		event := base
		event.Code = fallback.TamperDetected
		events = c.openOnce(events, event, now, c.cfg.FallbackDebounce)
	}
	if reading.SiteOutage != nil && *reading.SiteOutage {
		event := base
		event.AssetID = "site"
		event.Code = fallback.SiteOutage
		events = c.openOnce(events, event, now, c.cfg.FallbackDebounce)
	}
	return events
}

func (c *Collector) openOnce(events []fallback.Event, event fallback.Event, now time.Time, debounce time.Duration) []fallback.Event {
	key := conditionKey{assetID: event.AssetID, code: event.Code}
	state, exists := c.conditions[key]
	if !exists {
		state = conditionState{activeSince: now}
	}
	if state.opened || now.Sub(state.activeSince) < debounce {
		c.conditions[key] = state
		return events
	}
	c.conditions[key] = state
	return append(events, event)
}

func (c *Collector) markOpened(event fallback.Event) {
	key := conditionKey{assetID: event.AssetID, code: event.Code}
	if state, ok := c.conditions[key]; ok {
		state.opened = true
		c.conditions[key] = state
	}
}

func (c *Collector) resolveRecovered(ctx context.Context, reading domain.Reading, now time.Time) error {
	var result error
	if reading.AssetType == domain.AssetBatteryBMS && reading.InternalTemperature < c.cfg.BatteryOverheatC {
		result = errors.Join(result, c.resolve(ctx, reading.DeviceID, fallback.BatteryOverheat, now))
	}
	if reading.AssetType == domain.AssetSolarInverter && !reading.InverterFailed && !reading.InverterHardFault {
		result = errors.Join(result, c.resolve(ctx, reading.DeviceID, fallback.InverterFailure, now))
	}
	if !reading.TamperDetected {
		result = errors.Join(result, c.resolve(ctx, reading.DeviceID, fallback.TamperDetected, now))
	}
	if reading.SiteOutage != nil && !*reading.SiteOutage {
		result = errors.Join(result, c.resolve(ctx, "site", fallback.SiteOutage, now))
	}
	return result
}

func (c *Collector) resolve(ctx context.Context, assetID string, code fallback.Code, now time.Time) error {
	delete(c.conditions, conditionKey{assetID: assetID, code: code})
	if c.fallback == nil {
		return nil
	}
	return c.fallback.ResolveFallbackIncident(ctx, assetID, code, now)
}
