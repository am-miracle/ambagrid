use chrono::{DateTime, Utc};
use sqlx::{PgPool, Row, postgres::PgRow, query};
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
            RETURNING alert_id, asset_id, site_id, severity, status, reason, opened_at,
                resolved_at, resolution_note, resolved_by
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

        row_to_alert(row)
    }

    async fn find_open_alert(&self, asset_id: &str) -> Result<Option<Alert>, PortError> {
        let row = query(
            r#"
            SELECT alert_id, asset_id, site_id, severity, status, reason, opened_at,
                resolved_at, resolution_note, resolved_by
            FROM alerts
            WHERE asset_id = $1 AND status = 'open'
            ORDER BY opened_at DESC
            LIMIT 1
            "#,
        )
        .bind(asset_id)
        .fetch_optional(&self.pool)
        .await
        .map_err(PortError::storage)?;

        row.map(row_to_alert).transpose()
    }

    async fn resolve_alert(
        &self,
        alert_id: Uuid,
        resolved_at: DateTime<Utc>,
        resolution_note: &str,
        resolved_by: &str,
    ) -> Result<Alert, PortError> {
        let row = query(
            r#"
            UPDATE alerts
            SET status = 'resolved', resolved_at = $2, resolution_note = $3, resolved_by = $4
            WHERE alert_id = $1 AND status = 'open'
            RETURNING alert_id, asset_id, site_id, severity, status, reason, opened_at,
                resolved_at, resolution_note, resolved_by
            "#,
        )
        .bind(alert_id)
        .bind(resolved_at)
        .bind(resolution_note)
        .bind(resolved_by)
        .fetch_optional(&self.pool)
        .await
        .map_err(PortError::storage)?
        .ok_or_else(|| PortError::message(format!("alert {alert_id} is not open")))?;

        row_to_alert(row)
    }
}

fn row_to_alert(row: PgRow) -> Result<Alert, PortError> {
    Ok(Alert {
        alert_id: row.get::<Uuid, _>("alert_id"),
        asset_id: row.get("asset_id"),
        site_id: row.get("site_id"),
        severity: parse_severity(row.get::<String, _>("severity").as_str())?,
        status: parse_status(row.get::<String, _>("status").as_str())?,
        reason: row.get("reason"),
        opened_at: row.get::<DateTime<Utc>, _>("opened_at"),
        resolved_at: row.get::<Option<DateTime<Utc>>, _>("resolved_at"),
        resolution_note: row.get("resolution_note"),
        resolved_by: row.get("resolved_by"),
    })
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
