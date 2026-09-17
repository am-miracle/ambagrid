use serde::{Deserialize, Serialize};

use crate::domain::alert::Severity;

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct AlertOpenedPayload {
    pub alert_id: String,
    pub asset_id: String,
    pub site_id: String,
    pub severity: AlertSeverity,
    pub reason: String,
    pub opened_at_utc: i64,
    pub source_event_id: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct AlertResolvedPayload {
    pub alert_id: String,
    pub asset_id: String,
    pub site_id: String,
    pub severity: AlertSeverity,
    pub reason: String,
    pub opened_at_utc: i64,
    pub resolved_at_utc: i64,
    pub resolution_note: String,
    pub resolved_by: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Serialize)]
pub enum AlertSeverity {
    #[serde(rename = "SEVERITY_INFO")]
    Info,
    #[serde(rename = "SEVERITY_WARNING")]
    Warning,
    #[serde(rename = "SEVERITY_CRITICAL")]
    Critical,
}

impl From<Severity> for AlertSeverity {
    fn from(value: Severity) -> Self {
        match value {
            Severity::Info => Self::Info,
            Severity::Warning => Self::Warning,
            Severity::Critical => Self::Critical,
        }
    }
}
