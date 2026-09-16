use std::str::FromStr;

use chrono::{DateTime, Utc};
use uuid::Uuid;

use crate::domain::actor::ResolutionActor;

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

// as_str and from_str stay adjacent so the persisted spelling of a variant
// can't drift from the one that parses it back.
impl Severity {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Info => "info",
            Self::Warning => "warning",
            Self::Critical => "critical",
        }
    }
}

impl FromStr for Severity {
    type Err = UnknownAlertValue;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value {
            "info" => Ok(Self::Info),
            "warning" => Ok(Self::Warning),
            "critical" => Ok(Self::Critical),
            _ => Err(UnknownAlertValue::Severity(value.to_string())),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AlertStatus {
    Open,
    Resolved,
}

impl FromStr for AlertStatus {
    type Err = UnknownAlertValue;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value {
            "open" => Ok(Self::Open),
            "resolved" => Ok(Self::Resolved),
            _ => Err(UnknownAlertValue::Status(value.to_string())),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum UnknownAlertValue {
    #[error("unknown alert severity: {0}")]
    Severity(String),
    #[error("unknown alert status: {0}")]
    Status(String),
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn severity_round_trips_through_its_persisted_spelling() {
        for severity in [Severity::Info, Severity::Warning, Severity::Critical] {
            assert_eq!(severity.as_str().parse::<Severity>().unwrap(), severity);
        }
    }

    #[test]
    fn severity_rejects_an_unknown_value() {
        assert_eq!(
            "bogus".parse::<Severity>().unwrap_err(),
            UnknownAlertValue::Severity("bogus".to_string())
        );
    }

    #[test]
    fn alert_status_parses_its_persisted_spellings() {
        assert_eq!("open".parse::<AlertStatus>().unwrap(), AlertStatus::Open);
        assert_eq!(
            "resolved".parse::<AlertStatus>().unwrap(),
            AlertStatus::Resolved
        );
    }

    #[test]
    fn alert_status_rejects_an_unknown_value() {
        assert_eq!(
            "closed".parse::<AlertStatus>().unwrap_err(),
            UnknownAlertValue::Status("closed".to_string())
        );
    }
}
