use chrono::{DateTime, Utc};
use uuid::Uuid;

use crate::{
    domain::{
        alert::Alert,
        operator::{OperatorId, ResolutionActor},
    },
    ports::{AlertRepository, PortError, ResolveAlertPermission},
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ResolveAlertInput {
    pub alert_id: Uuid,
    pub resolution_note: String,
    pub resolved_by: OperatorId,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AllowedResolveOperators {
    allowed_operator_ids: Vec<OperatorId>,
}

impl AllowedResolveOperators {
    pub fn new(allowed_operator_ids: Vec<OperatorId>) -> Self {
        Self {
            allowed_operator_ids,
        }
    }
}

impl ResolveAlertPermission for AllowedResolveOperators {
    async fn authorize_resolve_alert(&self, operator_id: &OperatorId) -> Result<(), PortError> {
        if self
            .allowed_operator_ids
            .iter()
            .any(|allowed| allowed == operator_id)
        {
            Ok(())
        } else {
            Err(PortError::message(format!(
                "operator {} is not authorized to resolve alerts",
                operator_id.as_str()
            )))
        }
    }
}

pub struct ResolveAlert<'a, R, P> {
    pub store: &'a R,
    pub permissions: &'a P,
}

impl<'a, R, P> ResolveAlert<'a, R, P>
where
    R: AlertRepository,
    P: ResolveAlertPermission,
{
    pub fn new(store: &'a R, permissions: &'a P) -> Self {
        Self { store, permissions }
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
        let resolved_by = input.resolved_by;

        self.permissions
            .authorize_resolve_alert(&resolved_by)
            .await?;

        self.store
            .resolve_alert(
                input.alert_id,
                resolved_at,
                resolution_note.as_str(),
                ResolutionActor::Operator(resolved_by),
            )
            .await
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
        resolve_calls: Mutex<Vec<Uuid>>,
    }

    impl AlertRepository for FakeAlertRepository {
        async fn resolve_alert(
            &self,
            alert_id: Uuid,
            resolved_at: DateTime<Utc>,
            resolution_note: &str,
            resolved_by: ResolutionActor,
        ) -> Result<Alert, PortError> {
            self.resolve_calls.lock().unwrap().push(alert_id);
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
            alert.resolved_by = Some(resolved_by);
            Ok(alert)
        }
    }

    struct FakePermissions {
        allow: bool,
    }

    impl ResolveAlertPermission for FakePermissions {
        async fn authorize_resolve_alert(&self, operator_id: &OperatorId) -> Result<(), PortError> {
            if self.allow {
                Ok(())
            } else {
                Err(PortError::message(format!(
                    "operator {} is not authorized to resolve alerts",
                    operator_id.as_str()
                )))
            }
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
    async fn resolves_alert() {
        let alert = open_alert();
        let alert_id = alert.alert_id;
        let resolved_at = Utc::now();
        let store = FakeAlertRepository {
            alert: Mutex::new(Some(alert)),
            resolve_calls: Mutex::default(),
        };
        let permissions = FakePermissions { allow: true };
        let action = ResolveAlert::new(&store, &permissions);

        let resolved = action
            .execute_at(
                ResolveAlertInput {
                    alert_id,
                    resolution_note: "checked by field operator".to_string(),
                    resolved_by: OperatorId::new("operator-0101").unwrap(),
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
        assert_eq!(
            resolved.resolved_by.as_ref().map(ResolutionActor::as_str),
            Some("operator-0101")
        );
    }

    #[tokio::test]
    async fn trims_operator_resolution_inputs() {
        let alert = open_alert();
        let alert_id = alert.alert_id;
        let store = FakeAlertRepository {
            alert: Mutex::new(Some(alert)),
            resolve_calls: Mutex::default(),
        };
        let permissions = FakePermissions { allow: true };
        let action = ResolveAlert::new(&store, &permissions);

        let resolved = action
            .execute_at(
                ResolveAlertInput {
                    alert_id,
                    resolution_note: "  replaced cooling fan  ".to_string(),
                    resolved_by: OperatorId::new("  operator-0101  ").unwrap(),
                },
                Utc::now(),
            )
            .await
            .unwrap();

        assert_eq!(
            resolved.resolution_note.as_deref(),
            Some("replaced cooling fan")
        );
        assert_eq!(
            resolved.resolved_by.as_ref().map(ResolutionActor::as_str),
            Some("operator-0101")
        );
    }

    #[tokio::test]
    async fn rejects_empty_resolution_inputs() {
        let store = FakeAlertRepository::default();
        let permissions = FakePermissions { allow: true };
        let action = ResolveAlert::new(&store, &permissions);

        let err = action
            .execute_at(
                ResolveAlertInput {
                    alert_id: Uuid::new_v4(),
                    resolution_note: " ".to_string(),
                    resolved_by: OperatorId::new("operator-0101").unwrap(),
                },
                Utc::now(),
            )
            .await
            .unwrap_err();

        assert_eq!(err.to_string(), "resolution_note must not be empty");
    }

    #[tokio::test]
    async fn rejects_unauthorized_operator_before_mutating() {
        let alert = open_alert();
        let alert_id = alert.alert_id;
        let store = FakeAlertRepository {
            alert: Mutex::new(Some(alert)),
            resolve_calls: Mutex::default(),
        };
        let permissions = FakePermissions { allow: false };
        let action = ResolveAlert::new(&store, &permissions);

        let err = action
            .execute_at(
                ResolveAlertInput {
                    alert_id,
                    resolution_note: "handled manually".to_string(),
                    resolved_by: OperatorId::new("operator-0101").unwrap(),
                },
                Utc::now(),
            )
            .await
            .unwrap_err();

        assert_eq!(
            err.to_string(),
            "operator operator-0101 is not authorized to resolve alerts"
        );
        assert!(store.resolve_calls.lock().unwrap().is_empty());
    }

    #[tokio::test]
    async fn configured_permission_allows_only_listed_operators() {
        let permissions =
            AllowedResolveOperators::new(vec![OperatorId::new("operator-0101").unwrap()]);

        permissions
            .authorize_resolve_alert(&OperatorId::new("operator-0101").unwrap())
            .await
            .unwrap();
        let err = permissions
            .authorize_resolve_alert(&OperatorId::new("operator-9999").unwrap())
            .await
            .unwrap_err();

        assert_eq!(
            err.to_string(),
            "operator operator-9999 is not authorized to resolve alerts"
        );
    }
}
