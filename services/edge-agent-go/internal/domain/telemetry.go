package domain

import "time"

// Reading is a vendor-neutral sample produced by a hardware adapter.
type Reading struct {
	DeviceID            string
	AssetType           AssetType
	TakenAt             time.Time
	Metrics             *ElectricalMetrics
	InternalTemperature float64
	RelayClosed         bool
	BatterySOCPct       float64
	SolarIrradiance     float64
	InverterFailed      bool
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

// TelemetryPayload is the normalized JSON contract accepted by ingestion-go.
type TelemetryPayload struct {
	DeviceID            string             `json:"device_id"`
	DeviceType          string             `json:"device_type"`
	TimestampUTC        int64              `json:"timestamp_utc"`
	SiteID              string             `json:"site_id"`
	Metrics             *ElectricalMetrics `json:"metrics,omitempty"`
	InternalTemperature float64            `json:"internal_temperature"`
	RelayClosed         bool               `json:"relay_closed"`
	BatterySOCPct       float64            `json:"battery_soc_pct"`
	SolarIrradiance     float64            `json:"solar_irradiance"`
}
