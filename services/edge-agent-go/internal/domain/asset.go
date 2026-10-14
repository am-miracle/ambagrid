package domain

type AssetType string

const (
	AssetSmartMeter    AssetType = "smart_meter"
	AssetBatteryBMS    AssetType = "battery_bms"
	AssetSolarInverter AssetType = "solar_inverter"
)

func (a AssetType) Valid() bool {
	return a == AssetSmartMeter || a == AssetBatteryBMS || a == AssetSolarInverter
}

func (a AssetType) DeviceTypeName() string {
	switch a {
	case AssetSmartMeter:
		return "DEVICE_TYPE_SMART_METER"
	case AssetBatteryBMS:
		return "DEVICE_TYPE_BATTERY_BMS"
	case AssetSolarInverter:
		return "DEVICE_TYPE_SOLAR_INVERTER"
	default:
		return "DEVICE_TYPE_UNSPECIFIED"
	}
}

func (a AssetType) TopicSegment() string {
	switch a {
	case AssetSmartMeter:
		return "smartmeter"
	case AssetBatteryBMS:
		return "battery_bms"
	case AssetSolarInverter:
		return "solar_inverter"
	default:
		return "unknown"
	}
}
