use chrono::{DateTime, Utc};
use uuid::Uuid;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Severity {
    Info,
    Warning,
    Critical,
}

impl Severity {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Info => "info",
            Self::Warning => "warning",
            Self::Critical => "critical",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AlertStatus {
    Open,
    Resolved,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Alert {
    pub alert_id: Uuid,
    pub asset_id: String,
    pub site_id: String,
    pub severity: Severity,
    pub status: AlertStatus,
    pub reason: String,
    pub opened_at: DateTime<Utc>,
    pub resolved_at: Option<DateTime<Utc>>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AlertDecision {
    pub asset_id: String,
    pub site_id: String,
    pub severity: Severity,
    pub reason: String,
    pub opened_at: DateTime<Utc>,
    pub source_event_id: Option<String>,
}
