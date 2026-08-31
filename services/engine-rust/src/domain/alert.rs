use chrono::{DateTime, Utc};
use uuid::Uuid;

use crate::domain::operator::ResolutionActor;

#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub struct AlertKind(String);

impl AlertKind {
    pub fn new(value: impl Into<String>) -> Self {
        Self(value.into())
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl From<&str> for AlertKind {
    fn from(value: &str) -> Self {
        Self::new(value)
    }
}

impl From<String> for AlertKind {
    fn from(value: String) -> Self {
        Self::new(value)
    }
}

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
    pub kind: AlertKind,
    pub severity: Severity,
    pub status: AlertStatus,
    pub reason: String,
    pub opened_at: DateTime<Utc>,
    pub source_event_id: Option<String>,
    pub resolved_at: Option<DateTime<Utc>>,
    pub resolution_note: Option<String>,
    pub resolved_by: Option<ResolutionActor>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AlertDecision {
    pub asset_id: String,
    pub site_id: String,
    pub kind: AlertKind,
    pub severity: Severity,
    pub reason: String,
    pub opened_at: DateTime<Utc>,
    pub source_event_id: Option<String>,
}
