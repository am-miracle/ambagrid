// simulator supplies deterministic meter, battery, and inverter readings.
package simulator

import (
	"context"
	"math"
	"sync"
	"time"

	"edge-agent-go/internal/domain"
)

type Source struct {
	mu    sync.Mutex
	cycle uint64
	now   func() time.Time
}

func New() *Source {
	return &Source{now: time.Now}
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

	return []domain.Reading{
		{
			DeviceID:  "meter-01",
			AssetType: domain.AssetSmartMeter,
			TakenAt:   takenAt,
			Metrics: &domain.ElectricalMetrics{
				Voltage: &voltage, Current: &current, ActivePower: &power,
				Frequency: &frequency, TotalKWh: &totalKWh,
			},
			InternalTemperature: 36 + math.Sin(phase),
			RelayClosed:         true,
		},
		{
			DeviceID:            "battery-01",
			AssetType:           domain.AssetBatteryBMS,
			TakenAt:             takenAt,
			InternalTemperature: 31 + math.Sin(phase/2),
			RelayClosed:         true,
			BatterySOCPct:       72 + 8*math.Sin(phase/4),
		},
		{
			DeviceID:            "inverter-01",
			AssetType:           domain.AssetSolarInverter,
			TakenAt:             takenAt,
			InternalTemperature: 42 + 2*math.Sin(phase/2),
			RelayClosed:         true,
			SolarIrradiance:     650 + 100*math.Sin(phase/3),
		},
	}, nil
}
