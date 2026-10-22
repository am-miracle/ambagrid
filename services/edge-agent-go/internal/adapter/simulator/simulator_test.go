package simulator_test

import (
	"context"
	"testing"

	"edge-agent-go/internal/adapter/simulator"
	"edge-agent-go/internal/domain"
)

func TestCriticalFaultScenarioIsExplicitAndDeterministic(t *testing.T) {
	readings, err := simulator.New("site-01", simulator.WithCriticalFaults()).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var battery, inverter *domain.Reading
	for i := range readings {
		switch readings[i].AssetType {
		case domain.AssetBatteryBMS:
			battery = &readings[i]
		case domain.AssetSolarInverter:
			inverter = &readings[i]
		}
	}
	if battery == nil || battery.InternalTemperature != 68.2 || battery.SiteOutage == nil || !*battery.SiteOutage {
		t.Fatalf("battery fault reading = %+v", battery)
	}
	if inverter == nil || !inverter.InverterHardFault {
		t.Fatalf("inverter fault reading = %+v", inverter)
	}
}
