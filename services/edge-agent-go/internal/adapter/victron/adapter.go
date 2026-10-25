// Package victron reads a battery monitor exposed by a Cerbo GX Modbus-TCP service.
package victron

import (
	"context"
	"encoding/json"
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
	SiteID, DeviceID string
	UnitID           byte
	Now              func() time.Time
}
type Adapter struct {
	cfg       Config
	registers Registers
}

func New(cfg Config, registers Registers) (*Adapter, error) {
	if cfg.SiteID == "" || cfg.DeviceID == "" || registers == nil {
		return nil, fmt.Errorf("victron site, device and register transport are required")
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
	// Read individually: intermediate registers may not exist on this battery.
	values := make([]uint16, 4)
	for i, address := range []uint16{259, 261, 262, 266} {
		value, err := a.registers.ReadHolding(ctx, a.cfg.UnitID, address)
		if err != nil {
			return nil, adapter.ContextError(fmt.Errorf("victron register %d: %w", address, err))
		}
		// Victron marks unavailable unsigned/signed values with their maximum value.
		if (i == 0 || i == 3) && value == 0xffff || (i == 1 || i == 2) && value == 0x7fff {
			return nil, fmt.Errorf("%w: victron register %d unavailable", adapter.ErrInvalidResponse, address)
		}
		values[i] = value
	}
	voltage, current := float64(values[0])/100, float64(int16(values[1]))/10
	temp, soc := float64(int16(values[2]))/10, float64(values[3])/10
	if soc > 100 {
		return nil, fmt.Errorf("%w: battery SoC exceeds 100%%", adapter.ErrInvalidResponse)
	}
	at := a.cfg.Now().UTC()
	metrics := &domain.ElectricalMetrics{Voltage: &voltage, Current: &current}
	payload, err := json.Marshal(map[string]any{"device_id": a.cfg.DeviceID, "device_type": domain.AssetBatteryBMS.DeviceTypeName(), "site_id": a.cfg.SiteID, "timestamp_utc": at.Unix(), "metrics": metrics, "internal_temperature": temp, "battery_soc_pct": soc})
	if err != nil {
		return nil, err
	}
	return []domain.Reading{{DeviceID: a.cfg.DeviceID, AssetType: domain.AssetBatteryBMS, TakenAt: at, LastSeenAt: at, Payload: payload, Metrics: metrics, InternalTemperature: temp, BatterySOCPct: soc}}, nil
}
