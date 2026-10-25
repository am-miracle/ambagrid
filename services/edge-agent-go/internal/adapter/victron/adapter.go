// Package victron reads a battery monitor exposed by a Cerbo GX Modbus-TCP service.
package victron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
)

// Registers is the Modbus holding-register transport. UnitID is the battery
// service's ID from the GX Available services screen, not the system ID 100.
type Registers interface {
	ReadHolding(context.Context, byte, uint16) (uint16, error)
}
type Config struct {
	SiteID, DeviceID  string
	UnitID            byte
	Now               func() time.Time
	OptionalRegisters []uint16
}
type Adapter struct {
	cfg       Config
	registers Registers
}

func New(cfg Config, registers Registers) (*Adapter, error) {
	if cfg.SiteID == "" || cfg.DeviceID == "" || registers == nil {
		return nil, fmt.Errorf("victron site, device and register transport are required")
	}
	if cfg.OptionalRegisters == nil {
		cfg.OptionalRegisters = []uint16{261, 262, 266}
	}
	for _, address := range cfg.OptionalRegisters {
		if address != 259 && address != 261 && address != 262 && address != 266 {
			return nil, fmt.Errorf("unknown optional Victron register %d", address)
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Adapter{cfg: cfg, registers: registers}, nil
}
func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{DeviceType: domain.AssetBatteryBMS, Manufacturer: "victron", Model: "Cerbo GX battery service", Capabilities: adapter.CapabilitySet{Telemetry: true}}
}
func (a *Adapter) Read(ctx context.Context) ([]domain.Reading, error) {

	optional := map[uint16]bool{}
	for _, address := range a.cfg.OptionalRegisters {
		optional[address] = true
	}
	values := map[uint16]*float64{}
	for _, field := range []struct {
		address uint16
		scale   float64
		signed  bool
	}{{259, 100, false}, {261, 10, true}, {262, 10, true}, {266, 10, false}} {
		value, err := a.registers.ReadHolding(ctx, a.cfg.UnitID, field.address)
		if err != nil {
			if optional[field.address] && errors.Is(err, ErrRegisterUnavailable) {
				continue
			}
			return nil, adapter.ContextError(fmt.Errorf("victron register %d: %w", field.address, err))
		}
		unavailable := !field.signed && value == 0xffff || field.signed && value == 0x7fff
		if unavailable {
			if optional[field.address] {
				continue
			}
			return nil, fmt.Errorf("%w: required Victron register %d unavailable", adapter.ErrInvalidResponse, field.address)
		}
		measured := float64(value) / field.scale
		if field.signed {
			measured = float64(int16(value)) / field.scale
		}
		if field.address == 266 && measured > 100 {
			return nil, fmt.Errorf("%w: battery SoC exceeds 100%%", adapter.ErrInvalidResponse)
		}
		values[field.address] = &measured
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: no available Victron measurements", adapter.ErrInvalidResponse)
	}
	at := a.cfg.Now().UTC()
	metrics := &domain.ElectricalMetrics{Voltage: values[259], Current: values[261]}
	reading := domain.Reading{DeviceID: a.cfg.DeviceID, AssetType: domain.AssetBatteryBMS, TakenAt: at, LastSeenAt: at, Metrics: metrics, InternalTemperatureMissing: values[262] == nil, BatterySOCMissing: values[266] == nil}
	payload := map[string]any{"device_id": a.cfg.DeviceID, "device_type": domain.AssetBatteryBMS.DeviceTypeName(), "site_id": a.cfg.SiteID, "timestamp_utc": at.Unix(), "metrics": metrics}
	if v := values[262]; v != nil {
		reading.InternalTemperature = *v
		payload["internal_temperature"] = *v
	}
	if v := values[266]; v != nil {
		reading.BatterySOCPct = *v
		payload["battery_soc_pct"] = *v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	reading.Payload = raw
	return []domain.Reading{reading}, nil
}
