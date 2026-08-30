use chrono::{DateTime, Utc};
use uuid::Uuid;

use crate::{
    domain::alert::Alert,
    ports::{AlertEvents, AlertRepository, PortError},
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ResolveAlertInput {
    pub alert_id: Uuid,
    pub resolution_note: String,
    pub resolved_by: String,
}

pub struct ResolveAlert<'a, R, E> {
    pub store: &'a R,
    pub events: &'a E,
}

impl<'a, R, E> ResolveAlert<'a, R, E>
where
    R: AlertRepository,
    E: AlertEvents,
{
    pub fn new(store: &'a R, events: &'a E) -> Self {
        Self { store, events }
    }

    pub async fn execute(&self, input: ResolveAlertInput) -> Result<Alert, PortError> {
        self.execute_at(input, Utc::now()).await
    }

    async fn execute_at(
        &self,
        input: ResolveAlertInput,
        resolved_at: DateTime<Utc>,
    ) -> Result<Alert, PortError> {
        let resolution_note = required("resolution_note", input.resolution_note)?;
        let resolved_by = required("resolved_by", input.resolved_by)?;

        let alert = self
            .store
            .resolve_alert(
                input.alert_id,
                resolved_at,
                resolution_note.as_str(),
                resolved_by.as_str(),
            )
            .await?;

        if let Err(err) = self.events.alert_resolved(&alert).await {
            tracing::warn!(error = %err, alert_id = %alert.alert_id, "failed to publish alert.resolved event");
        }

        Ok(alert)
    }
}

fn required(name: &'static str, value: String) -> Result<String, PortError> {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        Err(PortError::message(format!("{name} must not be empty")))
    } else {
        Ok(trimmed.to_string())
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use crate::domain::alert::{AlertKind, AlertStatus, Severity};

    use super::*;

    #[derive(Default)]
    struct FakeAlertRepository {
        alert: Mutex<Option<Alert>>,
    }

    impl AlertRepository for FakeAlertRepository {
        async fn resolve_alert(
            &self,
            alert_id: Uuid,
            resolved_at: DateTime<Utc>,
            resolution_note: &str,
            resolved_by: &str,
        ) -> Result<Alert, PortError> {
            let mut alert = self
                .alert
                .lock()
                .unwrap()
                .take()
                .ok_or_else(|| PortError::message(format!("alert {alert_id} is not open")))?;

            if alert.alert_id != alert_id || alert.status != AlertStatus::Open {
                return Err(PortError::message(format!("alert {alert_id} is not open")));
            }

            alert.status = AlertStatus::Resolved;
            alert.resolved_at = Some(resolved_at);
            alert.resolution_note = Some(resolution_note.to_string());
            alert.resolved_by = Some(resolved_by.to_string());
            Ok(alert)
        }
    }

    #[derive(Default)]
    struct FakeEvents {
        resolved: Mutex<Vec<Alert>>,
        fail: bool,
    }

    impl AlertEvents for FakeEvents {
        async fn alert_opened(&self, _alert: &Alert) -> Result<(), PortError> {
            Ok(())
        }

        async fn alert_resolved(&self, alert: &Alert) -> Result<(), PortError> {
            if self.fail {
                return Err(PortError::message("broker unavailable"));
            }

            self.resolved.lock().unwrap().push(alert.clone());
            Ok(())
        }
    }

    fn open_alert() -> Alert {
        Alert {
            alert_id: Uuid::new_v4(),
            asset_id: "met-0101".to_string(),
            site_id: "ng-kaji-01".to_string(),
            kind: AlertKind::from("internal_temperature"),
            severity: Severity::Critical,
            status: AlertStatus::Open,
            reason: "internal_temperature_high:72.4C>=threshold:70.0C".to_string(),
            opened_at: Utc::now(),
            source_event_id: None,
            resolved_at: None,
            resolution_note: None,
            resolved_by: None,
        }
    }

    #[tokio::test]
    async fn resolves_alert_and_publishes_resolved_event() {
        let alert = open_alert();
        let alert_id = alert.alert_id;
        let resolved_at = Utc::now();
        let store = FakeAlertRepository {
            alert: Mutex::new(Some(alert)),
        };
        let events = FakeEvents::default();
        let action = ResolveAlert::new(&store, &events);

        let resolved = action
            .execute_at(
                ResolveAlertInput {
                    alert_id,
                    resolution_note: "checked by field operator".to_string(),
                    resolved_by: "operator-0101".to_string(),
                },
                resolved_at,
            )
            .await
            .unwrap();

        assert_eq!(resolved.alert_id, alert_id);
        assert_eq!(resolved.status, AlertStatus::Resolved);
        assert_eq!(resolved.resolved_at, Some(resolved_at));
        assert_eq!(
            resolved.resolution_note.as_deref(),
            Some("checked by field operator")
        );
        assert_eq!(resolved.resolved_by.as_deref(), Some("operator-0101"));
        assert_eq!(events.resolved.lock().unwrap().len(), 1);
    }

    #[tokio::test]
    async fn trims_operator_resolution_inputs() {
        let alert = open_alert();
        let alert_id = alert.alert_id;
        let store = FakeAlertRepository {
            alert: Mutex::new(Some(alert)),
        };
        let events = FakeEvents::default();
        let action = ResolveAlert::new(&store, &events);

        let resolved = action
            .execute_at(
                ResolveAlertInput {
                    alert_id,
                    resolution_note: "  replaced cooling fan  ".to_string(),
                    resolved_by: "  operator-0101  ".to_string(),
                },
                Utc::now(),
            )
            .await
            .unwrap();

        assert_eq!(
            resolved.resolution_note.as_deref(),
            Some("replaced cooling fan")
        );
        assert_eq!(resolved.resolved_by.as_deref(), Some("operator-0101"));
    }

    #[tokio::test]
    async fn rejects_empty_resolution_inputs() {
        let store = FakeAlertRepository::default();
        let events = FakeEvents::default();
        let action = ResolveAlert::new(&store, &events);

        let err = action
            .execute_at(
                ResolveAlertInput {
                    alert_id: Uuid::new_v4(),
                    resolution_note: " ".to_string(),
                    resolved_by: "operator-0101".to_string(),
                },
                Utc::now(),
            )
            .await
            .unwrap_err();

        assert_eq!(err.to_string(), "resolution_note must not be empty");
    }

    #[tokio::test]
    async fn resolution_succeeds_even_when_event_publish_fails() {
        let alert = open_alert();
        let alert_id = alert.alert_id;
        let store = FakeAlertRepository {
            alert: Mutex::new(Some(alert)),
        };
        let events = FakeEvents {
            resolved: Mutex::default(),
            fail: true,
        };
        let action = ResolveAlert::new(&store, &events);

        let result = action
            .execute_at(
                ResolveAlertInput {
                    alert_id,
                    resolution_note: "handled manually".to_string(),
                    resolved_by: "operator-0101".to_string(),
                },
                Utc::now(),
            )
            .await;

        assert!(result.is_ok());
        assert!(events.resolved.lock().unwrap().is_empty());
    }
}
