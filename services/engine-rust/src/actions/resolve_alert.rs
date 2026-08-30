use chrono::{DateTime, Utc};
use uuid::Uuid;

use crate::{
    domain::alert::Alert,
    ports::{AlertRepository, PortError},
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ResolveAlertInput {
    pub alert_id: Uuid,
    pub resolution_note: String,
    pub resolved_by: String,
}

pub async fn execute(
    alerts: &impl AlertRepository,
    input: &ResolveAlertInput,
    resolved_at: DateTime<Utc>,
) -> Result<Alert, PortError> {
    alerts
        .resolve_alert(
            input.alert_id,
            resolved_at,
            &input.resolution_note,
            &input.resolved_by,
        )
        .await
}
