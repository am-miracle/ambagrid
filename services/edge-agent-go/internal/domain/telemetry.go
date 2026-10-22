package domain

import "time"

// Reading is a vendor-neutral sample produced by a hardware adapter.
// Payload holds the canonical AmbaGrid JSON that the adapter produced;
// the collector preserves it unchanged in the queued event.
type Reading struct {
	DeviceID            string
	AssetType           AssetType
	TakenAt             time.Time
	Payload             []byte
	Metrics             *ElectricalMetrics
	InternalTemperature float64
	RelayClosed         bool
	BatterySOCPct       float64
	SolarIrradiance     float64
	InverterFailed      bool
	InverterHardFault   bool
	TamperDetected      bool
	SiteOutage          *bool
}

type ElectricalMetrics struct {
	Voltage     *float64 `json:"voltage,omitempty"`
	Current     *float64 `json:"current,omitempty"`
	ActivePower *float64 `json:"active_power,omitempty"`
	Frequency   *float64 `json:"frequency,omitempty"`
	TotalKWh    *float64 `json:"total_kwh,omitempty"`
}
