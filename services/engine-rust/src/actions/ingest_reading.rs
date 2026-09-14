use crate::{
    domain::{
        alert::{Alert, AlertKind},
        asset::Reading,
        operator::ResolutionActor,
        rules::{INTERNAL_TEMPERATURE_ALERT_KIND, ThresholdPolicy},
    },
    ports::{IngestRepository, PolicyOutcome, PortError},
};

#[derive(Debug, Clone, PartialEq)]
pub struct IngestResult {
    pub asset_id: String,
    pub alert_opened: Option<Alert>,
    pub alert_resolved: Option<Alert>,
}

pub struct IngestReading<'a, S> {
    pub store: &'a S,
    pub policy: ThresholdPolicy,
}

impl<'a, S> IngestReading<'a, S>
where
    S: IngestRepository,
{
    pub fn new(store: &'a S) -> Self {
        Self {
            store,
            policy: ThresholdPolicy::default(),
        }
    }

    pub async fn execute(&self, reading: Reading) -> Result<IngestResult, PortError> {
        let outcome = match self.policy.evaluate(&reading) {
            Some(decision) => PolicyOutcome::Open(decision),
            None => match self.policy.recovered(&reading) {
                Some(resolution_note) => PolicyOutcome::Resolve {
                    kind: AlertKind::from(INTERNAL_TEMPERATURE_ALERT_KIND),
                    resolution_note,
                    resolved_by: ResolutionActor::System,
                },
                None => PolicyOutcome::Unchanged,
            },
        };

        let write = self.store.ingest(&reading, outcome).await?;

        Ok(IngestResult {
            asset_id: reading.asset.asset_id,
            alert_opened: write.alert_opened,
            alert_resolved: write.alert_resolved,
        })
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use chrono::Utc;
    use uuid::Uuid;

    use crate::domain::alert::{Alert, AlertDecision, AlertKind, AlertStatus, Severity};
    use crate::{
        domain::asset::{Asset, AssetState, Reading, SmartMeterState},
        ports::IngestWrite,
    };

    use super::*;

    // Mirrors what PostgresIngestRepository does inside its transaction:
    // upsert bookkeeping plus the open/resolve duplicate-guard, all in one
    // place, since that's now one port method instead of three.
    #[derive(Default)]
    struct FakeIngestRepository {
        assets: Mutex<Vec<String>>,
        readings: Mutex<Vec<String>>,
        alerts: Mutex<Vec<Alert>>,
    }

    impl IngestRepository for FakeIngestRepository {
        async fn ingest(
            &self,
            reading: &Reading,
            outcome: PolicyOutcome,
        ) -> Result<IngestWrite, PortError> {
            self.assets
                .lock()
                .unwrap()
                .push(reading.asset.asset_id.clone());
            self.readings
                .lock()
                .unwrap()
                .push(reading.asset.asset_id.clone());

            let write = match outcome {
                PolicyOutcome::Open(decision) => {
                    let mut alerts = self.alerts.lock().unwrap();
                    let already_open = alerts.iter().any(|alert| {
                        alert.asset_id == decision.asset_id
                            && alert.kind == decision.kind
                            && alert.status == AlertStatus::Open
                    });

                    let alert_opened = if already_open {
                        None
                    } else {
                        let alert = Alert {
                            alert_id: Uuid::new_v4(),
                            asset_id: decision.asset_id.clone(),
                            site_id: decision.site_id.clone(),
                            kind: decision.kind.clone(),
                            severity: decision.severity,
                            status: AlertStatus::Open,
                            reason: decision.reason.clone(),
                            opened_at: decision.opened_at,
                            source_event_id: decision.source_event_id.clone(),
                            resolved_at: None,
                            resolution_note: None,
                            resolved_by: None,
                        };
                        alerts.push(alert.clone());
                        Some(alert)
                    };

                    IngestWrite {
                        alert_opened,
                        alert_resolved: None,
                    }
                }
                PolicyOutcome::Resolve {
                    kind,
                    resolution_note,
                    resolved_by,
                } => {
                    let mut alerts = self.alerts.lock().unwrap();
                    let open_alert = alerts.iter_mut().find(|alert| {
                        alert.asset_id == reading.asset.asset_id
                            && alert.kind == kind
                            && alert.status == AlertStatus::Open
                    });

                    let alert_resolved = open_alert.map(|alert| {
                        alert.status = AlertStatus::Resolved;
                        alert.resolved_at = Some(reading.observed_at);
                        alert.resolution_note = Some(resolution_note);
                        alert.resolved_by = Some(resolved_by);
                        alert.clone()
                    });

                    IngestWrite {
                        alert_opened: None,
                        alert_resolved,
                    }
                }
                PolicyOutcome::Unchanged => IngestWrite::default(),
            };

            Ok(write)
        }
    }

    #[tokio::test]
    async fn ingests_reading_and_opens_alert_when_policy_fires() {
        let store = FakeIngestRepository::default();
        let action = IngestReading::new(&store);
        let observed_at = Utc::now();

        let result = action
            .execute(Reading {
                asset: Asset {
                    asset_id: "met-0101".to_string(),
                    site_id: "ng-kaji-01".to_string(),
                    internal_temperature: Some(75.0),
                    last_seen_at: observed_at,
                },
                observed_at,
                source_event_id: Some("telemetry.ingested:0:42".to_string()),
                state: AssetState::SmartMeter(SmartMeterState::default()),
            })
            .await
            .unwrap();

        assert_eq!(result.asset_id, "met-0101");
        assert!(result.alert_opened.is_some());
        assert!(result.alert_resolved.is_none());
        assert_eq!(store.assets.lock().unwrap().len(), 1);
        assert_eq!(store.readings.lock().unwrap().len(), 1);
        assert_eq!(store.alerts.lock().unwrap().len(), 1);
        let opened = result.alert_opened.unwrap();
        assert_eq!(
            opened.source_event_id.as_deref(),
            Some("telemetry.ingested:0:42")
        );
    }

    #[tokio::test]
    async fn repeated_abnormal_readings_do_not_open_duplicate_alerts() {
        let store = FakeIngestRepository::default();
        let action = IngestReading::new(&store);
        let asset = Asset {
            asset_id: "met-0101".to_string(),
            site_id: "ng-kaji-01".to_string(),
            internal_temperature: Some(75.0),
            last_seen_at: Utc::now(),
        };

        let first = action
            .execute(Reading {
                asset: asset.clone(),
                observed_at: Utc::now(),
                source_event_id: None,
                state: AssetState::SmartMeter(SmartMeterState::default()),
            })
            .await
            .unwrap();

        let second = action
            .execute(Reading {
                asset: asset.clone(),
                observed_at: Utc::now(),
                source_event_id: None,
                state: AssetState::SmartMeter(SmartMeterState::default()),
            })
            .await
            .unwrap();

        assert!(first.alert_opened.is_some());
        assert!(
            second.alert_opened.is_none(),
            "should not open a second alert while one is already open"
        );
        assert_eq!(store.alerts.lock().unwrap().len(), 1);
    }

    fn sample_decision(kind: &str) -> AlertDecision {
        AlertDecision {
            asset_id: "met-0101".to_string(),
            site_id: "ng-kaji-01".to_string(),
            kind: AlertKind::from(kind),
            severity: Severity::Critical,
            reason: format!("{kind}_high"),
            opened_at: Utc::now(),
            source_event_id: Some(format!("event-{kind}")),
        }
    }

    fn sample_reading() -> Reading {
        Reading {
            asset: Asset {
                asset_id: "met-0101".to_string(),
                site_id: "ng-kaji-01".to_string(),
                internal_temperature: Some(75.0),
                last_seen_at: Utc::now(),
            },
            observed_at: Utc::now(),
            source_event_id: None,
            state: AssetState::SmartMeter(SmartMeterState::default()),
        }
    }

    // Regression test for a bug where duplicate-suppression and resolution
    // were both scoped only by asset_id: a second, unrelated problem on the
    // same asset would be silently swallowed by the first alert's presence,
    // and a recovery reading for one condition could resolve an alert for a
    // completely different one.
    #[tokio::test]
    async fn distinct_alert_kinds_on_the_same_asset_are_independent() {
        let store = FakeIngestRepository::default();
        let reading = sample_reading();

        let temperature_write = store
            .ingest(
                &reading,
                PolicyOutcome::Open(sample_decision("internal_temperature")),
            )
            .await
            .unwrap();
        let battery_write = store
            .ingest(
                &reading,
                PolicyOutcome::Open(sample_decision("battery_soc_low")),
            )
            .await
            .unwrap();

        assert!(
            temperature_write.alert_opened.is_some(),
            "first alert kind should open"
        );
        assert!(
            battery_write.alert_opened.is_some(),
            "a different alert kind on the same asset should not be suppressed \
             by the first one already being open"
        );
        assert_eq!(store.alerts.lock().unwrap().len(), 2);

        let resolve_write = store
            .ingest(
                &reading,
                PolicyOutcome::Resolve {
                    kind: AlertKind::from("internal_temperature"),
                    resolution_note: "internal_temperature_recovered".to_string(),
                    resolved_by: ResolutionActor::System,
                },
            )
            .await
            .unwrap();

        let resolved = resolve_write
            .alert_resolved
            .expect("the temperature alert should resolve");
        assert_eq!(resolved.kind.as_str(), "internal_temperature");
        assert_eq!(
            resolved.source_event_id.as_deref(),
            Some("event-internal_temperature")
        );

        let alerts = store.alerts.lock().unwrap();
        let battery_alert = alerts
            .iter()
            .find(|alert| alert.kind.as_str() == "battery_soc_low")
            .unwrap();
        assert_eq!(
            battery_alert.status,
            AlertStatus::Open,
            "resolving the temperature alert must not touch the unrelated battery alert"
        );
    }

    #[tokio::test]
    async fn resolves_open_alert_when_reading_recovers() {
        let store = FakeIngestRepository::default();
        let action = IngestReading::new(&store);
        let asset = Asset {
            asset_id: "met-0101".to_string(),
            site_id: "ng-kaji-01".to_string(),
            internal_temperature: Some(75.0),
            last_seen_at: Utc::now(),
        };

        let opened = action
            .execute(Reading {
                asset: asset.clone(),
                observed_at: Utc::now(),
                source_event_id: None,
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
                source_event_id: None,
                state: AssetState::SmartMeter(SmartMeterState::default()),
            })
            .await
            .unwrap();

        let resolved = recovered.alert_resolved.unwrap();
        assert_eq!(resolved.alert_id, alert_id);
        assert_eq!(resolved.status, AlertStatus::Resolved);
        assert_eq!(
            resolved.resolved_by.as_ref().map(ResolutionActor::as_str),
            Some("system")
        );
    }
}
