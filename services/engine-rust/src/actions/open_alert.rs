use crate::{
    domain::alert::{Alert, AlertDecision},
    ports::{AlertRepository, PortError},
};

pub async fn execute(
    alerts: &impl AlertRepository,
    decision: &AlertDecision,
) -> Result<Alert, PortError> {
    alerts.open_alert(decision).await
}
