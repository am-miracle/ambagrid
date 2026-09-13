// Defines assets and their latest type-specific state.
package domain

import (
	"fmt"
	"time"
)

type AssetType string

const (
	AssetTypeSmartMeter    AssetType = "smart_meter"
	AssetTypeBatteryBMS    AssetType = "battery_bms"
	AssetTypeSolarInverter AssetType = "solar_inverter"
)

func ParseAssetType(value string) (AssetType, error) {
	switch AssetType(value) {
	case AssetTypeSmartMeter, AssetTypeBatteryBMS, AssetTypeSolarInverter:
		return AssetType(value), nil
	default:
		return "", fmt.Errorf("unknown asset_type: %q", value)
	}
}

// Asset contains shared fields and one type-specific state block.
type Asset struct {
	AssetID             string
	SiteID              string
	AssetType           AssetType
	InternalTemperature *float32
	LastSeenAt          time.Time
	UpdatedAt           time.Time

	SmartMeter    *SmartMeterState
	BatteryBMS    *BatteryBMSState
	SolarInverter *SolarInverterState
}

type SmartMeterState struct {
	ReportedHouseholdID *string
	RelayClosed         *bool
	Voltage             *float32
	Current             *float32
	ActivePower         *float32
	Frequency           *float32
	TotalKWh            *float64
	UpdatedAt           time.Time
}

type BatteryBMSState struct {
	BatterySOCPct *float32
	UpdatedAt     time.Time
}

type SolarInverterState struct {
	SolarIrradiance *float32
	UpdatedAt       time.Time
}

// AssetFilter narrows an asset listing. A nil field means "no filter".
type AssetFilter struct {
	SiteID    *string
	AssetType *AssetType
}
