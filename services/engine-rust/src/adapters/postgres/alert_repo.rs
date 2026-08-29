use chrono::{DateTime, Utc};
use sqlx::{PgPool, Row, query};
use uuid::Uuid;

use crate::{
    domain::alert::{Alert, AlertDecision, AlertStatus, Severity},
    ports::{AlertRepository, PortError},
};

#[derive(Debug, Clone)]
pub struct PostgresAlertRepository {
    pool: PgPool,
}

impl PostgresAlertRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }
}

impl AlertRepository for PostgresAlertRepository {
    async fn open_alert(&self, decision: &AlertDecision) -> Result<Alert, PortError> {
        let row = query(
            r#"
            INSERT INTO alerts (
                asset_id, site_id, severity, status, reason, opened_at
            )
            VALUES ($1, $2, $3, 'open', $4, $5)
            RETURNING alert_id, asset_id, site_id, severity, status, reason, opened_at, resolved_at
            "#,
        )
        .bind(&decision.asset_id)
        .bind(&decision.site_id)
        .bind(decision.severity.as_str())
        .bind(&decision.reason)
        .bind(decision.opened_at)
        .fetch_one(&self.pool)
        .await
        .map_err(PortError::storage)?;

        Ok(Alert {
            alert_id: row.get::<Uuid, _>("alert_id"),
            asset_id: row.get("asset_id"),
            site_id: row.get("site_id"),
            severity: parse_severity(row.get::<String, _>("severity").as_str())?,
            status: parse_status(row.get::<String, _>("status").as_str())?,
            reason: row.get("reason"),
            opened_at: row.get::<DateTime<Utc>, _>("opened_at"),
            resolved_at: row.get::<Option<DateTime<Utc>>, _>("resolved_at"),
        })
    }
}

fn parse_severity(value: &str) -> Result<Severity, PortError> {
    match value {
        "info" => Ok(Severity::Info),
        "warning" => Ok(Severity::Warning),
        "critical" => Ok(Severity::Critical),
        _ => Err(PortError::message(format!(
            "unknown alert severity: {value}"
        ))),
    }
}

fn parse_status(value: &str) -> Result<AlertStatus, PortError> {
    match value {
        "open" => Ok(AlertStatus::Open),
        "resolved" => Ok(AlertStatus::Resolved),
        _ => Err(PortError::message(format!("unknown alert status: {value}"))),
    }
}
