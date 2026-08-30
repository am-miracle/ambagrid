use crate::domain::{
    alert::{AlertDecision, Severity},
    asset::{AssetType, Reading},
};

pub const INTERNAL_TEMPERATURE_ALERT_KIND: &str = "internal_temperature";

// Per-asset-type critical ceilings. These are rough values pulled from public
// standards/datasheets, not AmbaGrid's actual deployed hardware specs — swap
// in real numbers once hardware/product confirms them:
//   - smart meter: IEC 62052-11's component/dry-heat operating envelope tops
//     out around 65-70C.
//   - battery BMS: LFP packs are commonly rated safe for continuous operation
//     up to ~55-60C, well below their thermal-runaway onset (~116C+) so a BMS
//     has time to react.
//   - solar inverter: most datasheets derate starting ~40-50C and hit full
//     thermal shutdown around 75-85C; 75C flags trouble before that shutdown.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct ThresholdPolicy {
    pub smart_meter_critical_c: f32,
    pub battery_bms_critical_c: f32,
    pub solar_inverter_critical_c: f32,
    pub recovery_margin_c: f32,
}

impl Default for ThresholdPolicy {
    fn default() -> Self {
        Self {
            smart_meter_critical_c: 70.0,
            battery_bms_critical_c: 55.0,
            solar_inverter_critical_c: 75.0,
            recovery_margin_c: 5.0,
        }
    }
}

impl ThresholdPolicy {
    fn critical_for(&self, asset_type: AssetType) -> f32 {
        match asset_type {
            AssetType::SmartMeter => self.smart_meter_critical_c,
            AssetType::BatteryBms => self.battery_bms_critical_c,
            AssetType::SolarInverter => self.solar_inverter_critical_c,
        }
    }

    pub fn evaluate(&self, reading: &Reading) -> Option<AlertDecision> {
        let temperature = reading.asset.internal_temperature?;
        let threshold = self.critical_for(reading.asset.asset_type);
        if temperature < threshold {
            return None;
        }

        Some(AlertDecision {
            asset_id: reading.asset.asset_id.clone(),
            site_id: reading.asset.site_id.clone(),
            kind: INTERNAL_TEMPERATURE_ALERT_KIND.to_string(),
            severity: Severity::Critical,
            reason: format!(
                "internal_temperature_high:{temperature:.1}C>=threshold:{threshold:.1}C"
            ),
            opened_at: reading.observed_at,
            source_event_id: None,
        })
    }

    // Whether the reading indicates recovery, i.e. an open alert on this
    // asset should be resolved. Missing temperature is treated as "unknown",
    // not "recovered", so a dropped sensor reading can't quietly close an
    // alert. Recovery uses a lower threshold than the one that opened the
    // alert (see `recovery_margin_c`), not the same boundary.
    pub fn recovered(&self, reading: &Reading) -> Option<String> {
        let temperature = reading.asset.internal_temperature?;
        let threshold = self.critical_for(reading.asset.asset_type) - self.recovery_margin_c;
        if temperature >= threshold {
            return None;
        }

        Some(format!(
            "internal_temperature_recovered:{temperature:.1}C<threshold:{threshold:.1}C"
        ))
    }
}

#[cfg(test)]
mod tests {
    use chrono::Utc;

    use crate::domain::asset::{
        Asset, AssetState, AssetType, BatteryBmsState, Reading, SmartMeterState,
    };

    use super::*;

    #[test]
    fn opens_critical_alert_when_internal_temperature_crosses_threshold() {
        let reading = Reading {
            asset: Asset {
                asset_id: "met-0101".to_string(),
                site_id: "ng-kaji-01".to_string(),
                asset_type: AssetType::SmartMeter,
                internal_temperature: Some(72.4),
                last_seen_at: Utc::now(),
            },
            observed_at: Utc::now(),
            state: AssetState::SmartMeter(SmartMeterState::default()),
        };

        let decision = ThresholdPolicy::default().evaluate(&reading).unwrap();

        assert_eq!(decision.asset_id, "met-0101");
        assert_eq!(decision.site_id, "ng-kaji-01");
        assert_eq!(decision.severity, Severity::Critical);
        assert!(decision.reason.contains("internal_temperature_high"));
    }

    #[test]
    fn leaves_normal_temperature_alone() {
        let reading = Reading {
            asset: Asset {
                asset_id: "met-0101".to_string(),
                site_id: "ng-kaji-01".to_string(),
                asset_type: AssetType::SmartMeter,
                internal_temperature: Some(38.0),
                last_seen_at: Utc::now(),
            },
            observed_at: Utc::now(),
            state: AssetState::SmartMeter(SmartMeterState::default()),
        };

        assert!(ThresholdPolicy::default().evaluate(&reading).is_none());
    }

    #[test]
    fn battery_bms_trips_at_a_lower_threshold_than_a_smart_meter() {
        let reading = Reading {
            asset: Asset {
                asset_id: "bms-0101".to_string(),
                site_id: "ng-kaji-01".to_string(),
                asset_type: AssetType::BatteryBms,
                internal_temperature: Some(60.0),
                last_seen_at: Utc::now(),
            },
            observed_at: Utc::now(),
            state: AssetState::BatteryBms(BatteryBmsState::default()),
        };

        // 60C wouldn't trip the smart meter threshold, but it's above the
        // battery pack's lower ceiling.
        assert!(ThresholdPolicy::default().evaluate(&reading).is_some());
    }

    #[test]
    fn reports_recovery_when_temperature_drops_below_threshold() {
        let reading = Reading {
            asset: Asset {
                asset_id: "met-0101".to_string(),
                site_id: "ng-kaji-01".to_string(),
                asset_type: AssetType::SmartMeter,
                internal_temperature: Some(38.0),
                last_seen_at: Utc::now(),
            },
            observed_at: Utc::now(),
            state: AssetState::SmartMeter(SmartMeterState::default()),
        };

        let note = ThresholdPolicy::default().recovered(&reading).unwrap();
        assert!(note.contains("internal_temperature_recovered"));
    }

    #[test]
    fn does_not_recover_inside_the_hysteresis_gap() {
        let reading = Reading {
            asset: Asset {
                asset_id: "met-0101".to_string(),
                site_id: "ng-kaji-01".to_string(),
                asset_type: AssetType::SmartMeter,
                // Below the 70C open threshold but still above the 65C
                // recovery threshold (70 - 5C margin): the alert should stay
                // open rather than flap closed.
                internal_temperature: Some(67.0),
                last_seen_at: Utc::now(),
            },
            observed_at: Utc::now(),
            state: AssetState::SmartMeter(SmartMeterState::default()),
        };

        assert!(ThresholdPolicy::default().evaluate(&reading).is_none());
        assert!(ThresholdPolicy::default().recovered(&reading).is_none());
    }

    #[test]
    fn does_not_report_recovery_when_temperature_is_unknown() {
        let reading = Reading {
            asset: Asset {
                asset_id: "met-0101".to_string(),
                site_id: "ng-kaji-01".to_string(),
                asset_type: AssetType::SmartMeter,
                internal_temperature: None,
                last_seen_at: Utc::now(),
            },
            observed_at: Utc::now(),
            state: AssetState::SmartMeter(SmartMeterState::default()),
        };

        assert!(ThresholdPolicy::default().recovered(&reading).is_none());
    }
}
