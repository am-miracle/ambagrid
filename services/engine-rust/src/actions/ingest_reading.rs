use crate::{
    actions::open_alert,
    domain::{alert::Alert, asset::Reading, rules::ThresholdPolicy},
    ports::{AlertRepository, AssetRepository, PortError, ReadingsSink},
};

#[derive(Debug, Clone, PartialEq)]
pub struct IngestResult {
    pub asset_id: String,
    pub alert_opened: Option<Alert>,
}

pub struct IngestReading<'a, A, R, L> {
    pub assets: &'a A,
    pub readings: &'a R,
    pub alerts: &'a L,
    pub policy: ThresholdPolicy,
}

impl<'a, A, R, L> IngestReading<'a, A, R, L>
where
    A: AssetRepository,
    R: ReadingsSink,
    L: AlertRepository,
{
    pub fn new(assets: &'a A, readings: &'a R, alerts: &'a L) -> Self {
        Self {
            assets,
            readings,
            alerts,
            policy: ThresholdPolicy::default(),
        }
    }

    pub async fn execute(&self, reading: Reading) -> Result<IngestResult, PortError> {
        self.assets.upsert_asset(&reading.asset).await?;
        self.assets.upsert_state(&reading).await?;
        self.readings.append_reading(&reading).await?;

        let alert_opened = match self.policy.evaluate(&reading) {
            Some(decision) => Some(open_alert::execute(self.alerts, &decision).await?),
            None => None,
        };

        Ok(IngestResult {
            asset_id: reading.asset.asset_id,
            alert_opened,
        })
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use chrono::Utc;
    use uuid::Uuid;

    use crate::domain::{
        alert::{Alert, AlertDecision, AlertStatus},
        asset::{Asset, AssetState, AssetType, Reading, SmartMeterState},
    };

    use super::*;

    #[derive(Default)]
    struct FakeAssets {
        assets: Mutex<Vec<String>>,
        states: Mutex<Vec<String>>,
    }

    impl AssetRepository for FakeAssets {
        async fn upsert_asset(&self, asset: &Asset) -> Result<(), PortError> {
            self.assets.lock().unwrap().push(asset.asset_id.clone());
            Ok(())
        }

        async fn upsert_state(&self, reading: &Reading) -> Result<(), PortError> {
            self.states
                .lock()
                .unwrap()
                .push(reading.asset.asset_id.clone());
            Ok(())
        }
    }

    #[derive(Default)]
    struct FakeReadings {
        readings: Mutex<Vec<String>>,
    }

    impl ReadingsSink for FakeReadings {
        async fn append_reading(&self, reading: &Reading) -> Result<(), PortError> {
            self.readings
                .lock()
                .unwrap()
                .push(reading.asset.asset_id.clone());
            Ok(())
        }
    }

    #[derive(Default)]
    struct FakeAlerts {
        alerts: Mutex<Vec<AlertDecision>>,
    }

    impl AlertRepository for FakeAlerts {
        async fn open_alert(&self, decision: &AlertDecision) -> Result<Alert, PortError> {
            self.alerts.lock().unwrap().push(decision.clone());
            Ok(Alert {
                alert_id: Uuid::new_v4(),
                asset_id: decision.asset_id.clone(),
                site_id: decision.site_id.clone(),
                severity: decision.severity,
                status: AlertStatus::Open,
                reason: decision.reason.clone(),
                opened_at: decision.opened_at,
                resolved_at: None,
            })
        }
    }

    #[tokio::test]
    async fn ingests_reading_and_opens_alert_when_policy_fires() {
        let assets = FakeAssets::default();
        let readings = FakeReadings::default();
        let alerts = FakeAlerts::default();
        let action = IngestReading::new(&assets, &readings, &alerts);
        let observed_at = Utc::now();

        let result = action
            .execute(Reading {
                asset: Asset {
                    asset_id: "met-0101".to_string(),
                    site_id: "ng-kaji-01".to_string(),
                    asset_type: AssetType::SmartMeter,
                    internal_temperature: Some(75.0),
                    last_seen_at: observed_at,
                },
                observed_at,
                state: AssetState::SmartMeter(SmartMeterState::default()),
            })
            .await
            .unwrap();

        assert_eq!(result.asset_id, "met-0101");
        assert!(result.alert_opened.is_some());
        assert_eq!(assets.assets.lock().unwrap().len(), 1);
        assert_eq!(assets.states.lock().unwrap().len(), 1);
        assert_eq!(readings.readings.lock().unwrap().len(), 1);
        assert_eq!(alerts.alerts.lock().unwrap().len(), 1);
    }
}
