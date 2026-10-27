package modbus

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"edge-agent-go/internal/adapter"
	"edge-agent-go/internal/domain"
)

// Registers is a snapshot of raw register words by function and address. A
// register missing from the snapshot was unavailable on the device.
type Registers map[Function]map[uint16]uint16

// Device identifies the physical device a map is read from.
type Device struct {
	SiteID   string
	DeviceID string
}

// Decode turns one register snapshot into a reading without touching the network.
func Decode(m Map, regs Registers, device Device, at time.Time) (domain.Reading, error) {
	at = at.UTC()
	reading := domain.Reading{
		DeviceID: device.DeviceID, AssetType: m.DeviceType, TakenAt: at, LastSeenAt: at,
		Metrics: &domain.ElectricalMetrics{},
	}
	payload := map[string]any{
		"device_id": device.DeviceID, "device_type": m.DeviceType.DeviceTypeName(),
		"site_id": device.SiteID, "timestamp_utc": at.Unix(),
	}
	for _, p := range m.Points {
		value, ok := p.value(regs[p.Function])
		if !ok {
			if p.Optional {
				continue
			}
			return domain.Reading{}, fmt.Errorf("%w: required %s register %d unavailable", adapter.ErrInvalidResponse, p.Field, p.Address)
		}
		if p.Field == FieldBatterySOC && (value < 0 || value > 100) {
			return domain.Reading{}, fmt.Errorf("%w: battery SoC %v outside 0-100%%", adapter.ErrInvalidResponse, value)
		}
		switch p.Field {
		case FieldVoltage:
			reading.Metrics.Voltage = &value
		case FieldCurrent:
			reading.Metrics.Current = &value
		case FieldActivePower:
			reading.Metrics.ActivePower = &value
		case FieldFrequency:
			reading.Metrics.Frequency = &value
		case FieldTotalKWh:
			reading.Metrics.TotalKWh = &value
		case FieldInternalTemperature:
			reading.InternalTemperature = &value
			payload["internal_temperature"] = value
		case FieldBatterySOC:
			reading.BatterySOCPct = &value
			payload["battery_soc_pct"] = value
		case FieldSolarIrradiance:
			reading.SolarIrradiance = value
			payload["solar_irradiance"] = value
		}
	}
	payload["metrics"] = reading.Metrics
	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.Reading{}, fmt.Errorf("encode reading payload: %w", err)
	}
	reading.Payload = raw
	return reading, nil
}

// value reports false when a register is absent or holds the unavailable sentinel.
func (p Point) value(regs map[uint16]uint16) (float64, bool) {
	words := make([]uint16, p.Type.Words())
	for i := range words {
		word, ok := regs[p.Address+uint16(i)]
		if !ok {
			return 0, false
		}
		words[i] = word
	}
	raw := uint32(words[0])
	if len(words) == 2 {
		high, low := words[0], words[1]
		if p.WordOrder == LittleWordOrder {
			high, low = low, high
		}
		raw = uint32(high)<<16 | uint32(low)
	}
	if p.Unavailable != nil && raw == *p.Unavailable {
		return 0, false
	}
	var value float64
	switch p.Type {
	case U16, U32:
		value = float64(raw)
	case I16:
		value = float64(int16(raw))
	case I32:
		value = float64(int32(raw))
	case F32:
		value = float64(math.Float32frombits(raw))
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, false
		}
	}
	return value * p.Scale, true
}
