use chrono::{DateTime, Utc};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AssetType {
    SmartMeter,
    BatteryBms,
    SolarInverter,
}

impl AssetType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::SmartMeter => "smart_meter",
            Self::BatteryBms => "battery_bms",
            Self::SolarInverter => "solar_inverter",
        }
    }
}

#[derive(Debug, Clone, PartialEq)]
pub struct Asset {
    pub asset_id: String,
    pub site_id: String,
    pub internal_temperature: Option<f32>,
    pub last_seen_at: DateTime<Utc>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct Reading {
    pub asset: Asset,
    pub observed_at: DateTime<Utc>,
    pub state: AssetState,
}

#[derive(Debug, Clone, PartialEq)]
pub enum AssetState {
    SmartMeter(SmartMeterState),
    BatteryBms(BatteryBmsState),
    SolarInverter(SolarInverterState),
}

impl AssetState {
    pub fn asset_type(&self) -> AssetType {
        match self {
            Self::SmartMeter(_) => AssetType::SmartMeter,
            Self::BatteryBms(_) => AssetType::BatteryBms,
            Self::SolarInverter(_) => AssetType::SolarInverter,
        }
    }
}

impl Reading {
    pub fn asset_type(&self) -> AssetType {
        self.state.asset_type()
    }
}

#[derive(Debug, Clone, PartialEq, Default)]
pub struct SmartMeterState {
    pub reported_household_id: Option<String>,
    pub relay_closed: Option<bool>,
    pub voltage: Option<f32>,
    pub current: Option<f32>,
    pub active_power: Option<f32>,
    pub frequency: Option<f32>,
    pub total_kwh: Option<f64>,
}

#[derive(Debug, Clone, PartialEq, Default)]
pub struct BatteryBmsState {
    pub battery_soc_pct: Option<f32>,
}

#[derive(Debug, Clone, PartialEq, Default)]
pub struct SolarInverterState {
    pub solar_irradiance: Option<f32>,
}
