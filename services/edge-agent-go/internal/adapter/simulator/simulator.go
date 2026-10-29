// simulator supplies deterministic meter, battery, and inverter readings.
package simulator

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
)

var defaultCapabilities = adapter.Capabilities{
	DeviceType:   domain.AssetSmartMeter,
	Manufacturer: "ambagrid",
	Model:        "simulator",
	Capabilities: adapter.CapabilitySet{Telemetry: true, RelayControl: true, PrepaidCredit: true},
}

type Adapter struct {
	siteID       string
	device       *Device
	capabilities adapter.Capabilities

	commandSlot *adapter.Slot
}

type Option func(*Adapter)

func WithCriticalFaults() Option {
	return func(a *Adapter) { a.device.faults, a.device.tamper = true, true }
}

func WithCapabilities(capabilities adapter.CapabilitySet) Option {
	return func(a *Adapter) { a.capabilities.Capabilities = capabilities }
}

func New(siteID string, options ...Option) *Adapter {
	a := &Adapter{
		siteID:       siteID,
		device:       newDevice(),
		capabilities: defaultCapabilities,
		commandSlot:  adapter.NewSlot(),
	}
	for _, option := range options {
		option(a)
	}
	return a
}

func (a *Adapter) Device() *Device {
	return a.device
}

func (a *Adapter) Capabilities() adapter.Capabilities {
	return a.capabilities
}

func (a *Adapter) Read(ctx context.Context) ([]domain.Reading, error) {
	raw, err := a.device.Poll(ctx)
	if err != nil {
		return nil, adapter.ContextError(err)
	}
	var frames []frame
	if err := json.Unmarshal(raw, &frames); err != nil {
		return nil, fmt.Errorf("%w: %w", adapter.ErrInvalidResponse, err)
	}
	readings := make([]domain.Reading, 0, len(frames))
	for _, f := range frames {
		reading, err := normalize(f)
		if err != nil {
			return nil, err
		}
		if reading.Payload, err = marshalPayload(a.siteID, reading); err != nil {
			return nil, err
		}
		readings = append(readings, reading)
	}
	return readings, nil
}

func normalize(f frame) (domain.Reading, error) {
	r := domain.Reading{
		DeviceID:            f.Device,
		TakenAt:             time.UnixMilli(f.TsMs).UTC(),
		InternalTemperature: new(float64(f.TempDC) / 10),
		RelayClosed:         f.Relay == 1,
		TamperDetected:      f.Tamper == 1,
		InverterFailed:      f.Fault == faultSoft,
		InverterHardFault:   f.Fault == faultHard,
	}
	r.LastSeenAt = r.TakenAt
	if f.SeenMs != nil {
		r.LastSeenAt = time.UnixMilli(*f.SeenMs).UTC()
	}
	if f.Outage != nil {
		outage := *f.Outage == 1
		r.SiteOutage = &outage
	}
	switch f.Kind {
	case kindMeter:
		r.AssetType = domain.AssetSmartMeter
		r.Metrics = &domain.ElectricalMetrics{
			Voltage: unscale(f.VoltDV, 10), Current: unscale(f.CurrMA, 1000), ActivePower: unscale(f.PowerW, 1000),
			Frequency: unscale(f.FreqCHz, 100), TotalKWh: unscale(f.EnergyWh, 1000),
		}
	case kindBMS:
		r.AssetType = domain.AssetBatteryBMS
		if f.SoCPm != nil {
			r.BatterySOCPct = unscale(f.SoCPm, 10)
		}
	case kindInverter:
		r.AssetType = domain.AssetSolarInverter
		if f.IrrWm2 != nil {
			r.SolarIrradiance = float64(*f.IrrWm2)
		}
	default:
		return domain.Reading{}, fmt.Errorf("%w: device %q has unknown kind %q", adapter.ErrInvalidResponse, f.Device, f.Kind)
	}
	return r, nil
}

func unscale(v *int64, factor float64) *float64 {
	if v == nil {
		return nil
	}
	f := float64(*v) / factor
	return &f
}

func (a *Adapter) Execute(ctx context.Context, cmd adapter.Command) (adapter.CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return adapter.CommandResult{}, adapter.ContextError(err)
	}
	if err := a.capabilities.Supports(cmd); err != nil {
		return adapter.CommandResult{}, err
	}
	if err := cmd.Validate(); err != nil {
		return adapter.CommandResult{}, err
	}

	payload, err := json.Marshal(encodeCommand(cmd))
	if err != nil {
		return adapter.CommandResult{}, err
	}
	// The gateway's meter link carries one exchange at a time.
	release, err := a.commandSlot.Acquire(ctx)
	if err != nil {
		return adapter.CommandResult{}, err
	}
	defer release()
	if err := adapter.BeforeSend(ctx); err != nil {
		return adapter.CommandResult{}, err
	}
	raw, err := a.device.Send(ctx, payload)
	if err != nil {
		return adapter.CommandResult{}, adapter.ContextError(err)
	}
	var response vendorResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return adapter.CommandResult{}, fmt.Errorf("%w: %w", adapter.ErrInvalidResponse, err)
	}
	if !response.OK {
		return adapter.CommandResult{}, fmt.Errorf("%w: %s: %s", adapter.ErrDeviceRejected, response.Code, response.Message)
	}
	result := adapter.CommandResult{CommandID: cmd.ID, AcknowledgedAt: time.UnixMilli(response.TsMs).UTC()}
	if response.Relay != nil {
		closed := *response.Relay == 1
		result.RelayClosed = &closed
	}
	result.BalanceKWh = unscale(response.BalanceWh, 1000)
	if response.Tamper != nil {
		tamper := *response.Tamper == 1
		result.TamperDetected = &tamper
	}
	return result, nil
}

func encodeCommand(cmd adapter.Command) vendorCommand {
	v := vendorCommand{Ref: cmd.ID, Device: cmd.DeviceID}
	switch cmd.Type {
	case adapter.CommandCredit:
		v.Op = "credit"
		v.Wh = int64(math.Round(cmd.CreditKWh * 1000))
	case adapter.CommandDisconnect, adapter.CommandReconnect:
		closed := cmd.Type == adapter.CommandReconnect
		v.Op, v.Closed = "relay", &closed
	case adapter.CommandRequestStatus:
		v.Op = "status"
	}
	return v
}

type canonicalPayload struct {
	DeviceID            string                    `json:"device_id"`
	DeviceType          string                    `json:"device_type"`
	TimestampUTC        int64                     `json:"timestamp_utc"`
	LastSeenUTC         int64                     `json:"last_seen_utc"`
	SiteID              string                    `json:"site_id"`
	Metrics             *domain.ElectricalMetrics `json:"metrics,omitempty"`
	InternalTemperature *float64                  `json:"internal_temperature"`
	RelayClosed         bool                      `json:"relay_closed"`
	BatterySOCPct       *float64                  `json:"battery_soc_pct,omitempty"`
	SolarIrradiance     float64                   `json:"solar_irradiance"`
}

func marshalPayload(siteID string, r domain.Reading) ([]byte, error) {
	return json.Marshal(canonicalPayload{
		DeviceID:            r.DeviceID,
		DeviceType:          r.AssetType.DeviceTypeName(),
		TimestampUTC:        r.TakenAt.UTC().Unix(),
		LastSeenUTC:         r.LastSeenAt.UTC().Unix(),
		SiteID:              siteID,
		Metrics:             r.Metrics,
		InternalTemperature: r.InternalTemperature,
		RelayClosed:         r.RelayClosed,
		BatterySOCPct:       r.BatterySOCPct,
		SolarIrradiance:     r.SolarIrradiance,
	})
}
