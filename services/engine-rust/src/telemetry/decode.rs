use chrono::{DateTime, Utc};
use prost::Message;

use crate::{
    domain::asset::{
        Asset, AssetState, AssetType, BatteryBmsState, Reading, SmartMeterState, SolarInverterState,
    },
    telemetry::proto::{DeviceType, MetricPayload},
};

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum DecodeError {
    #[error("invalid telemetry protobuf: {0}")]
    InvalidProtobuf(String),
    #[error("unknown telemetry device_type: {0}")]
    UnknownDeviceType(i32),
    #[error("telemetry device_id must not be empty")]
    MissingDeviceId,
    #[error("telemetry site_id must not be empty")]
    MissingSiteId,
    #[error("telemetry metrics must be set for a smart meter payload")]
    MissingMetrics,
    #[error("invalid telemetry timestamp_utc: {0}")]
    InvalidTimestamp(i64),
    #[error("telemetry metric {0} must be finite")]
    InvalidMetric(&'static str),
}

pub fn decode_metric_payload(bytes: &[u8]) -> Result<Reading, DecodeError> {
    let payload = MetricPayload::decode(bytes)
        .map_err(|err| DecodeError::InvalidProtobuf(err.to_string()))?;
    reading_from_payload(payload)
}

pub fn reading_from_payload(payload: MetricPayload) -> Result<Reading, DecodeError> {
    let asset_id = required(payload.device_id, DecodeError::MissingDeviceId)?;
    let site_id = required(payload.site_id, DecodeError::MissingSiteId)?;
    let asset_type = asset_type(payload.device_type)?;
    let observed_at = DateTime::<Utc>::from_timestamp(payload.timestamp_utc, 0)
        .ok_or(DecodeError::InvalidTimestamp(payload.timestamp_utc))?;
    let internal_temperature = finite_f32("internal_temperature", payload.internal_temperature)?;

    let state = match asset_type {
        AssetType::SmartMeter => {
            let metrics = payload.metrics.ok_or(DecodeError::MissingMetrics)?;
            AssetState::SmartMeter(SmartMeterState {
                reported_household_id: optional_string(payload.household_id),
                relay_closed: Some(payload.relay_closed),
                voltage: finite_optional_f32("voltage", metrics.voltage)?,
                current: finite_optional_f32("current", metrics.current)?,
                active_power: finite_optional_f32("active_power", metrics.active_power)?,
                frequency: finite_optional_f32("frequency", metrics.frequency)?,
                total_kwh: finite_optional_f64("total_kwh", metrics.total_kwh)?,
            })
        }
        AssetType::BatteryBms => AssetState::BatteryBms(BatteryBmsState {
            battery_soc_pct: finite_f32("battery_soc_pct", payload.battery_soc_pct)?,
        }),
        AssetType::SolarInverter => AssetState::SolarInverter(SolarInverterState {
            solar_irradiance: finite_f32("solar_irradiance", payload.solar_irradiance)?,
        }),
    };

    Ok(Reading {
        asset: Asset {
            asset_id,
            site_id,
            asset_type,
            internal_temperature,
            last_seen_at: observed_at,
        },
        observed_at,
        state,
    })
}

fn asset_type(value: i32) -> Result<AssetType, DecodeError> {
    match DeviceType::try_from(value) {
        Ok(DeviceType::SmartMeter) => Ok(AssetType::SmartMeter),
        Ok(DeviceType::BatteryBms) => Ok(AssetType::BatteryBms),
        Ok(DeviceType::SolarInverter) => Ok(AssetType::SolarInverter),
        Ok(DeviceType::Unspecified) | Err(_) => Err(DecodeError::UnknownDeviceType(value)),
    }
}

fn required(value: String, error: DecodeError) -> Result<String, DecodeError> {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        Err(error)
    } else {
        Ok(trimmed.to_string())
    }
}

fn optional_string(value: String) -> Option<String> {
    let trimmed = value.trim();
    (!trimmed.is_empty()).then(|| trimmed.to_string())
}

fn finite_f32(name: &'static str, value: f32) -> Result<Option<f32>, DecodeError> {
    if value.is_finite() {
        Ok(Some(value))
    } else {
        Err(DecodeError::InvalidMetric(name))
    }
}

fn finite_f64(name: &'static str, value: f64) -> Result<Option<f64>, DecodeError> {
    if value.is_finite() {
        Ok(Some(value))
    } else {
        Err(DecodeError::InvalidMetric(name))
    }
}

// A meter not reporting this field (`None`) is left as-is; a reported value
// still has to be finite.
fn finite_optional_f32(name: &'static str, value: Option<f32>) -> Result<Option<f32>, DecodeError> {
    value
        .map(|value| finite_f32(name, value))
        .transpose()
        .map(Option::flatten)
}

fn finite_optional_f64(name: &'static str, value: Option<f64>) -> Result<Option<f64>, DecodeError> {
    value
        .map(|value| finite_f64(name, value))
        .transpose()
        .map(Option::flatten)
}

#[cfg(test)]
mod tests {
    use prost::Message;

    use crate::{
        domain::asset::{AssetState, AssetType},
        telemetry::proto::{ElectricalMetrics, MetricPayload},
    };

    use super::*;

    #[test]
    fn decodes_smart_meter_payload_into_ontology_reading() {
        let payload = MetricPayload {
            device_id: " met-0101 ".to_string(),
            device_type: DeviceType::SmartMeter as i32,
            timestamp_utc: 1_787_990_400,
            site_id: "ng-kaji-01".to_string(),
            household_id: "hh-009".to_string(),
            metrics: Some(ElectricalMetrics {
                voltage: Some(231.0),
                current: Some(4.2),
                active_power: Some(0.91),
                frequency: Some(50.0),
                total_kwh: Some(103.5),
            }),
            internal_temperature: 42.5,
            relay_closed: true,
            battery_soc_pct: 0.0,
            solar_irradiance: 0.0,
        };
        let mut bytes = Vec::new();
        payload.encode(&mut bytes).unwrap();

        let reading = decode_metric_payload(&bytes).unwrap();

        assert_eq!(reading.asset.asset_id, "met-0101");
        assert_eq!(reading.asset.site_id, "ng-kaji-01");
        assert_eq!(reading.asset.asset_type, AssetType::SmartMeter);
        assert_eq!(reading.asset.internal_temperature, Some(42.5));
        let AssetState::SmartMeter(state) = &reading.state else {
            panic!("expected smart meter state");
        };
        assert_eq!(state.voltage, Some(231.0));
        assert_eq!(state.total_kwh, Some(103.5));
    }

    #[test]
    fn leaves_unreported_metric_fields_as_none() {
        let payload = MetricPayload {
            device_id: "met-0101".to_string(),
            device_type: DeviceType::SmartMeter as i32,
            site_id: "ng-kaji-01".to_string(),
            metrics: Some(ElectricalMetrics {
                voltage: Some(231.0),
                current: None,
                ..Default::default()
            }),
            ..MetricPayload::default()
        };

        let reading = reading_from_payload(payload).unwrap();

        let AssetState::SmartMeter(state) = reading.state else {
            panic!("expected smart meter state");
        };
        assert_eq!(state.voltage, Some(231.0));
        assert_eq!(state.current, None);
    }

    #[test]
    fn rejects_unspecified_device_type() {
        let err = reading_from_payload(MetricPayload {
            device_id: "met-0101".to_string(),
            site_id: "ng-kaji-01".to_string(),
            ..MetricPayload::default()
        })
        .unwrap_err();

        assert_eq!(err, DecodeError::UnknownDeviceType(0));
    }

    #[test]
    fn rejects_smart_meter_payload_missing_metrics() {
        let err = reading_from_payload(MetricPayload {
            device_id: "met-0101".to_string(),
            device_type: DeviceType::SmartMeter as i32,
            site_id: "ng-kaji-01".to_string(),
            metrics: None,
            ..MetricPayload::default()
        })
        .unwrap_err();

        assert_eq!(err, DecodeError::MissingMetrics);
    }
}
