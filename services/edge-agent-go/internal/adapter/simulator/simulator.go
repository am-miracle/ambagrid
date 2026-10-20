// simulator supplies deterministic meter, battery, and inverter readings.
package simulator

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"time"

	"edge-agent-go/internal/domain"
)

type Source struct {
	siteID string
	mu     sync.Mutex
	cycle  uint64
	now    func() time.Time
}

func New(siteID string) *Source {
	return &Source{siteID: siteID, now: time.Now}
}

func (s *Source) Read(ctx context.Context) ([]domain.Reading, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	takenAt := s.now().UTC()
	phase := float64(s.cycle) / 8
	s.cycle++
	voltage := 230 + 3*math.Sin(phase)
	current := 6 + math.Sin(phase/2)
	power := voltage * current / 1000
	frequency := 50 + 0.04*math.Sin(phase/3)
	totalKWh := 1500 + float64(s.cycle)*power/720

	meter := domain.Reading{
		DeviceID:  "meter-01",
		AssetType: domain.AssetSmartMeter,
		TakenAt:   takenAt,
		Metrics: &domain.ElectricalMetrics{
			Voltage: &voltage, Current: &current, ActivePower: &power,
			Frequency: &frequency, TotalKWh: &totalKWh,
		},
		InternalTemperature: 36 + math.Sin(phase),
		RelayClosed:         true,
	}
	battery := domain.Reading{
		DeviceID:            "battery-01",
		AssetType:           domain.AssetBatteryBMS,
		TakenAt:             takenAt,
		InternalTemperature: 31 + math.Sin(phase/2),
		RelayClosed:         true,
		BatterySOCPct:       72 + 8*math.Sin(phase/4),
	}
	inverter := domain.Reading{
		DeviceID:            "inverter-01",
		AssetType:           domain.AssetSolarInverter,
		TakenAt:             takenAt,
		InternalTemperature: 42 + 2*math.Sin(phase/2),
		RelayClosed:         true,
		SolarIrradiance:     650 + 100*math.Sin(phase/3),
	}

	readings := []domain.Reading{meter, battery, inverter}
	for i := range readings {
		payload, err := marshalPayload(s.siteID, readings[i])
		if err != nil {
			return nil, err
		}
		readings[i].Payload = payload
	}
	return readings, nil
}

type canonicalPayload struct {
	DeviceID            string                    `json:"device_id"`
	DeviceType          string                    `json:"device_type"`
	TimestampUTC        int64                     `json:"timestamp_utc"`
	SiteID              string                    `json:"site_id"`
	Metrics             *domain.ElectricalMetrics `json:"metrics,omitempty"`
	InternalTemperature float64                   `json:"internal_temperature"`
	RelayClosed         bool                      `json:"relay_closed"`
	BatterySOCPct       float64                   `json:"battery_soc_pct"`
	SolarIrradiance     float64                   `json:"solar_irradiance"`
}

func marshalPayload(siteID string, r domain.Reading) ([]byte, error) {
	return json.Marshal(canonicalPayload{
		DeviceID:            r.DeviceID,
		DeviceType:          r.AssetType.DeviceTypeName(),
		TimestampUTC:        r.TakenAt.UTC().Unix(),
		SiteID:              siteID,
		Metrics:             r.Metrics,
		InternalTemperature: r.InternalTemperature,
		RelayClosed:         r.RelayClosed,
		BatterySOCPct:       r.BatterySOCPct,
		SolarIrradiance:     r.SolarIrradiance,
	})
}
