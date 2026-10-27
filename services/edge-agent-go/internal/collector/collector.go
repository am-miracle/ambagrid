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

type DebounceStore interface {
	LoadDebounceConditions(context.Context) ([]fallback.DebounceCondition, error)
	SaveDebounceCondition(ctx context.Context, assetID string, code fallback.Code, activeSince time.Time) error
	DeleteDebounceCondition(ctx context.Context, assetID string, code fallback.Code) error
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
	debounce   DebounceStore
	conditions map[conditionKey]conditionState
	restored   bool
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
	store, ok := queue.(DebounceStore)
	if !ok {
		return nil, errors.New("collector queue must support durable debounce state")
	}
	c.debounce = store
	return c, nil
}

// SetClock makes condition debounce deterministic for tests and simulations.
func (c *Collector) SetClock(now func() time.Time) {
	c.now = now
}

func (c *Collector) RestoreDebounceState(ctx context.Context) error {
	conditions, err := c.debounce.LoadDebounceConditions(ctx)
	if err != nil {
		return fmt.Errorf("load debounce conditions: %w", err)
	}
	restored := make(map[conditionKey]conditionState, len(conditions))
	for _, dc := range conditions {
		key := conditionKey{assetID: dc.AssetID, code: dc.Code}
		restored[key] = conditionState{activeSince: dc.ActiveSince, opened: dc.Opened}
	}
	c.conditions = restored
	c.restored = true
	return nil
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
	if !c.restored {
		if err := c.RestoreDebounceState(ctx); err != nil {
			return fmt.Errorf("restore debounce state: %w", err)
		}
	}
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
			criticalEvents, err := c.criticalEvents(ctx, reading, now)
			collectionErr = errors.Join(collectionErr, err)
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

func (c *Collector) criticalEvents(ctx context.Context, reading domain.Reading, now time.Time) ([]fallback.Event, error) {
	base := fallback.Event{SiteID: c.cfg.SiteID, AssetID: reading.DeviceID, OccurredAt: reading.TakenAt.UTC()}
	var events []fallback.Event
	var result error
	add := func(event fallback.Event, debounce time.Duration) {
		var err error
		events, err = c.openOnce(ctx, events, event, now, debounce)
		result = errors.Join(result, err)
	}
	if reading.AssetType == domain.AssetBatteryBMS && reading.InternalTemperature != nil && *reading.InternalTemperature >= c.cfg.BatteryOverheatC {
		event := base
		event.Code = fallback.BatteryOverheat
		temperature := *reading.InternalTemperature
		event.TemperatureC = &temperature
		add(event, 0)
	}
	if reading.AssetType == domain.AssetSolarInverter && (reading.InverterFailed || reading.InverterHardFault) {
		event := base
		event.Code = fallback.InverterFailure
		debounce := c.cfg.FallbackDebounce
		if reading.InverterHardFault {
			debounce = 0
		}
		add(event, debounce)
	}
	if reading.TamperDetected {
		event := base
		event.Code = fallback.TamperDetected
		add(event, c.cfg.FallbackDebounce)
	}
	if reading.SiteOutage != nil && *reading.SiteOutage {
		event := base
		event.AssetID = "site"
		event.Code = fallback.SiteOutage
		add(event, c.cfg.FallbackDebounce)
	}
	return events, result
}

func (c *Collector) openOnce(ctx context.Context, events []fallback.Event, event fallback.Event, now time.Time, debounce time.Duration) ([]fallback.Event, error) {
	key := conditionKey{assetID: event.AssetID, code: event.Code}
	state, exists := c.conditions[key]
	if !exists {
		state = conditionState{activeSince: now}
		if err := c.debounce.SaveDebounceCondition(ctx, event.AssetID, event.Code, now); err != nil {
			return events, fmt.Errorf("persist debounce condition for asset %q code %q: %w", event.AssetID, event.Code, err)
		}
	}
	if state.opened || now.Sub(state.activeSince) < debounce {
		c.conditions[key] = state
		return events, nil
	}
	c.conditions[key] = state
	return append(events, event), nil
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
	if reading.AssetType == domain.AssetBatteryBMS && reading.InternalTemperature != nil && *reading.InternalTemperature < c.cfg.BatteryOverheatC {
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
	key := conditionKey{assetID: assetID, code: code}
	if _, tracked := c.conditions[key]; tracked {
		if err := c.debounce.DeleteDebounceCondition(ctx, assetID, code); err != nil {
			return fmt.Errorf("delete debounce condition for asset %q code %q: %w", assetID, code, err)
		}
	}
	delete(c.conditions, key)
	if c.fallback == nil {
		return nil
	}
	return c.fallback.ResolveFallbackIncident(ctx, assetID, code, now)
}
