use crate::{
    domain::{alert::Alert, asset::Reading, rules::ThresholdPolicy},
    ports::{AlertEvents, IngestRepository, PolicyOutcome, PortError},
};

const RESOLVED_BY_SYSTEM: &str = "system";

#[derive(Debug, Clone, PartialEq)]
pub struct IngestResult {
    pub asset_id: String,
    pub alert_opened: Option<Alert>,
    pub alert_resolved: Option<Alert>,
}

pub struct IngestReading<'a, S, E> {
    pub store: &'a S,
    pub events: &'a E,
    pub policy: ThresholdPolicy,
}

impl<'a, S, E> IngestReading<'a, S, E>
where
    S: IngestRepository,
    E: AlertEvents,
{
    pub fn new(store: &'a S, events: &'a E) -> Self {
        Self {
            store,
            events,
            policy: ThresholdPolicy::default(),
        }
    }

    pub async fn execute(&self, reading: Reading) -> Result<IngestResult, PortError> {
        let outcome = match self.policy.evaluate(&reading) {
            Some(decision) => PolicyOutcome::Open(decision),
            None => match self.policy.recovered(&reading) {
                Some(resolution_note) => PolicyOutcome::Resolve {
                    resolution_note,
                    resolved_by: RESOLVED_BY_SYSTEM.to_string(),
                },
                None => PolicyOutcome::Unchanged,
            },
        };

        let write = self.store.ingest(&reading, outcome).await?;

        if let Some(alert) = &write.alert_opened {
            self.publish_opened(alert).await;
        }
        if let Some(alert) = &write.alert_resolved {
            self.publish_resolved(alert).await;
        }

        Ok(IngestResult {
            asset_id: reading.asset.asset_id,
            alert_opened: write.alert_opened,
            alert_resolved: write.alert_resolved,
        })
    }

    // Alert state in Postgres is the source of truth; a dropped or delayed
    // event just makes the ontology stream lag, so publish failures are
    // logged rather than failing the ingest.
    async fn publish_opened(&self, alert: &Alert) {
        if let Err(err) = self.events.alert_opened(alert).await {
            tracing::warn!(error = %err, alert_id = %alert.alert_id, "failed to publish alert.opened event");
        }
    }

    async fn publish_resolved(&self, alert: &Alert) {
        if let Err(err) = self.events.alert_resolved(alert).await {
            tracing::warn!(error = %err, alert_id = %alert.alert_id, "failed to publish alert.resolved event");
        }
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use chrono::Utc;
    use uuid::Uuid;

    use crate::domain::alert::{Alert, AlertStatus};
    use crate::{
        domain::asset::{Asset, AssetState, AssetType, Reading, SmartMeterState},
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
                        alert.asset_id == decision.asset_id && alert.status == AlertStatus::Open
                    });

                    let alert_opened = if already_open {
                        None
                    } else {
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
                        alerts.push(alert.clone());
                        Some(alert)
                    };

                    IngestWrite {
                        alert_opened,
                        alert_resolved: None,
                    }
                }
                PolicyOutcome::Resolve {
                    resolution_note,
                    resolved_by,
                } => {
                    let mut alerts = self.alerts.lock().unwrap();
                    let open_alert = alerts.iter_mut().find(|alert| {
                        alert.asset_id == reading.asset.asset_id
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

    #[derive(Default)]
    struct FakeEvents {
        opened: Mutex<Vec<Alert>>,
        resolved: Mutex<Vec<Alert>>,
    }

    impl AlertEvents for FakeEvents {
        async fn alert_opened(&self, alert: &Alert) -> Result<(), PortError> {
            self.opened.lock().unwrap().push(alert.clone());
            Ok(())
        }

        async fn alert_resolved(&self, alert: &Alert) -> Result<(), PortError> {
            self.resolved.lock().unwrap().push(alert.clone());
            Ok(())
        }
    }

    #[tokio::test]
    async fn ingests_reading_and_opens_alert_when_policy_fires() {
        let store = FakeIngestRepository::default();
        let events = FakeEvents::default();
        let action = IngestReading::new(&store, &events);
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
        assert_eq!(store.assets.lock().unwrap().len(), 1);
        assert_eq!(store.readings.lock().unwrap().len(), 1);
        assert_eq!(store.alerts.lock().unwrap().len(), 1);
        assert_eq!(events.opened.lock().unwrap().len(), 1);
        assert_eq!(events.resolved.lock().unwrap().len(), 0);
    }

    #[tokio::test]
    async fn repeated_abnormal_readings_do_not_open_duplicate_alerts() {
        let store = FakeIngestRepository::default();
        let events = FakeEvents::default();
        let action = IngestReading::new(&store, &events);
        let asset = Asset {
            asset_id: "met-0101".to_string(),
            site_id: "ng-kaji-01".to_string(),
            asset_type: AssetType::SmartMeter,
            internal_temperature: Some(75.0),
            last_seen_at: Utc::now(),
        };

        let first = action
            .execute(Reading {
                asset: asset.clone(),
                observed_at: Utc::now(),
                state: AssetState::SmartMeter(SmartMeterState::default()),
            })
            .await
            .unwrap();

        let second = action
            .execute(Reading {
                asset: asset.clone(),
                observed_at: Utc::now(),
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
        assert_eq!(events.opened.lock().unwrap().len(), 1);
    }

    #[tokio::test]
    async fn resolves_open_alert_when_reading_recovers() {
        let store = FakeIngestRepository::default();
        let events = FakeEvents::default();
        let action = IngestReading::new(&store, &events);
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
        assert_eq!(events.opened.lock().unwrap().len(), 1);
        assert_eq!(events.resolved.lock().unwrap().len(), 1);
    }

    // A publish failure must not fail the ingest: Postgres is the source of
    // truth for alert state, so a broken event stream should only be logged.
    #[tokio::test]
    async fn ingest_succeeds_even_when_event_publish_fails() {
        struct FailingEvents;

        impl AlertEvents for FailingEvents {
            async fn alert_opened(&self, _alert: &Alert) -> Result<(), PortError> {
                Err(PortError::message("broker unreachable"))
            }

            async fn alert_resolved(&self, _alert: &Alert) -> Result<(), PortError> {
                Err(PortError::message("broker unreachable"))
            }
        }

        let store = FakeIngestRepository::default();
        let events = FailingEvents;
        let action = IngestReading::new(&store, &events);

        let result = action
            .execute(Reading {
                asset: Asset {
                    asset_id: "met-0101".to_string(),
                    site_id: "ng-kaji-01".to_string(),
                    asset_type: AssetType::SmartMeter,
                    internal_temperature: Some(75.0),
                    last_seen_at: Utc::now(),
                },
                observed_at: Utc::now(),
                state: AssetState::SmartMeter(SmartMeterState::default()),
            })
            .await
            .unwrap();

        assert!(result.alert_opened.is_some());
    }
}
