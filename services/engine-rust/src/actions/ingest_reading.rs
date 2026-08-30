use crate::{
    actions::{
        open_alert,
        resolve_alert::{self, ResolveAlertInput},
    },
    domain::{alert::Alert, asset::Reading, rules::ThresholdPolicy},
    ports::{AlertRepository, AssetRepository, PortError, ReadingsSink},
};

const RESOLVED_BY_SYSTEM: &str = "system";

#[derive(Debug, Clone, PartialEq)]
pub struct IngestResult {
    pub asset_id: String,
    pub alert_opened: Option<Alert>,
    pub alert_resolved: Option<Alert>,
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

        let (alert_opened, alert_resolved) = match self.policy.evaluate(&reading) {
            Some(decision) => (
                Some(open_alert::execute(self.alerts, &decision).await?),
                None,
            ),
            None => (None, self.resolve_open_alert(&reading).await?),
        };

        Ok(IngestResult {
            asset_id: reading.asset.asset_id,
            alert_opened,
            alert_resolved,
        })
    }

    // Resolves the asset's open alert if the reading shows recovery. A
    // reading that neither trips nor recovers the policy (e.g. missing
    // temperature) leaves any open alert untouched.
    async fn resolve_open_alert(&self, reading: &Reading) -> Result<Option<Alert>, PortError> {
        let Some(resolution_note) = self.policy.recovered(reading) else {
            return Ok(None);
        };

        let Some(open_alert) = self.alerts.find_open_alert(&reading.asset.asset_id).await? else {
            return Ok(None);
        };

        let resolved = resolve_alert::execute(
            self.alerts,
            &ResolveAlertInput {
                alert_id: open_alert.alert_id,
                resolution_note,
                resolved_by: RESOLVED_BY_SYSTEM.to_string(),
            },
            reading.observed_at,
        )
        .await?;

        Ok(Some(resolved))
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
        alerts: Mutex<Vec<Alert>>,
    }

    impl AlertRepository for FakeAlerts {
        async fn open_alert(&self, decision: &AlertDecision) -> Result<Alert, PortError> {
            let alert = Alert {
                alert_id: Uuid::new_v4(),
                asset_id: decision.asset_id.clone(),
                site_id: decision.site_id.clone(),
                severity: decision.severity,
                status: AlertStatus::Open,
                reason: decision.reason.clone(),
                opened_at: decision.opened_at,
                resolved_at: None,
                resolution_note: None,
                resolved_by: None,
            };
            self.alerts.lock().unwrap().push(alert.clone());
            Ok(alert)
        }

        async fn find_open_alert(&self, asset_id: &str) -> Result<Option<Alert>, PortError> {
            Ok(self
                .alerts
                .lock()
                .unwrap()
                .iter()
                .find(|alert| alert.asset_id == asset_id && alert.status == AlertStatus::Open)
                .cloned())
        }

        async fn resolve_alert(
            &self,
            alert_id: Uuid,
            resolved_at: chrono::DateTime<Utc>,
            resolution_note: &str,
            resolved_by: &str,
        ) -> Result<Alert, PortError> {
            let mut alerts = self.alerts.lock().unwrap();
            let alert = alerts
                .iter_mut()
                .find(|alert| alert.alert_id == alert_id)
                .expect("resolving an alert that was never opened");
            alert.status = AlertStatus::Resolved;
            alert.resolved_at = Some(resolved_at);
            alert.resolution_note = Some(resolution_note.to_string());
            alert.resolved_by = Some(resolved_by.to_string());
            Ok(alert.clone())
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
        assert!(result.alert_resolved.is_none());
        assert_eq!(assets.assets.lock().unwrap().len(), 1);
        assert_eq!(assets.states.lock().unwrap().len(), 1);
        assert_eq!(readings.readings.lock().unwrap().len(), 1);
        assert_eq!(alerts.alerts.lock().unwrap().len(), 1);
    }

    #[tokio::test]
    async fn resolves_open_alert_when_reading_recovers() {
        let assets = FakeAssets::default();
        let readings = FakeReadings::default();
        let alerts = FakeAlerts::default();
        let action = IngestReading::new(&assets, &readings, &alerts);
        let asset = Asset {
            asset_id: "met-0101".to_string(),
            site_id: "ng-kaji-01".to_string(),
            asset_type: AssetType::SmartMeter,
            internal_temperature: Some(75.0),
            last_seen_at: Utc::now(),
        };

        let opened = action
            .execute(Reading {
                asset: asset.clone(),
                observed_at: Utc::now(),
                state: AssetState::SmartMeter(SmartMeterState::default()),
            })
            .await
            .unwrap();
        let alert_id = opened.alert_opened.unwrap().alert_id;

        let recovered = action
            .execute(Reading {
                asset: Asset {
                    internal_temperature: Some(38.0),
                    ..asset
                },
                observed_at: Utc::now(),
                state: AssetState::SmartMeter(SmartMeterState::default()),
            })
            .await
            .unwrap();

        let resolved = recovered.alert_resolved.unwrap();
        assert_eq!(resolved.alert_id, alert_id);
        assert_eq!(resolved.status, AlertStatus::Resolved);
        assert_eq!(resolved.resolved_by.as_deref(), Some("system"));
    }
}
