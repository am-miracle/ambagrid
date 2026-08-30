use crate::domain::{
    alert::{AlertDecision, Severity},
    asset::Reading,
};

#[derive(Debug, Clone, Copy, PartialEq)]
pub struct ThresholdPolicy {
    pub internal_temperature_critical_c: f32,
}

impl Default for ThresholdPolicy {
    fn default() -> Self {
        Self {
            internal_temperature_critical_c: 70.0,
        }
    }
}

impl ThresholdPolicy {
    pub fn evaluate(&self, reading: &Reading) -> Option<AlertDecision> {
        let temperature = reading.asset.internal_temperature?;
        if temperature < self.internal_temperature_critical_c {
            return None;
        }

        Some(AlertDecision {
            asset_id: reading.asset.asset_id.clone(),
            site_id: reading.asset.site_id.clone(),
            severity: Severity::Critical,
            reason: format!(
                "internal_temperature_high:{temperature:.1}C>=threshold:{:.1}C",
                self.internal_temperature_critical_c
            ),
            opened_at: reading.observed_at,
            source_event_id: None,
        })
    }

    // Whether the reading indicates recovery, i.e. an open alert on this
    // asset should be resolved. Missing temperature is treated as "unknown",
    // not "recovered", so a dropped sensor reading can't quietly close an
    // alert.
    pub fn recovered(&self, reading: &Reading) -> Option<String> {
        let temperature = reading.asset.internal_temperature?;
        if temperature >= self.internal_temperature_critical_c {
            return None;
        }

        Some(format!(
            "internal_temperature_recovered:{temperature:.1}C<threshold:{:.1}C",
            self.internal_temperature_critical_c
        ))
    }
}

#[cfg(test)]
mod tests {
    use chrono::Utc;

    use crate::domain::asset::{Asset, AssetState, AssetType, Reading, SmartMeterState};

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
