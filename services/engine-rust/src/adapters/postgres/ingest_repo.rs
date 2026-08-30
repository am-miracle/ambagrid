use chrono::{DateTime, Utc};
use sqlx::{PgPool, Postgres, Row, Transaction, postgres::PgRow, query};
use uuid::Uuid;

use crate::{
    domain::{
        alert::{Alert, AlertDecision, AlertStatus, Severity},
        asset::{AssetState, Reading},
    },
    ports::{IngestRepository, IngestWrite, PolicyOutcome, PortError},
};

#[derive(Debug, Clone)]
pub struct PostgresIngestRepository {
    pool: PgPool,
}

impl PostgresIngestRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }
}

impl IngestRepository for PostgresIngestRepository {
    // Everything below runs in one transaction: a mid-write failure rolls
    // back cleanly instead of leaving a partial commit for a retry to
    // duplicate (e.g. a reading recorded twice, or recorded without its
    // alert decision).
    async fn ingest(
        &self,
        reading: &Reading,
        outcome: PolicyOutcome,
    ) -> Result<IngestWrite, PortError> {
        let mut tx = self.pool.begin().await.map_err(PortError::storage)?;

        upsert_asset(&mut tx, reading).await?;
        upsert_state(&mut tx, reading).await?;
        append_reading(&mut tx, reading).await?;

        let write = match outcome {
            PolicyOutcome::Open(decision) => {
                let alert_opened = if find_open_alert(&mut tx, &decision.asset_id)
                    .await?
                    .is_some()
                {
                    None
                } else {
                    Some(open_alert(&mut tx, &decision).await?)
                };
                IngestWrite {
                    alert_opened,
                    alert_resolved: None,
                }
            }
            PolicyOutcome::Resolve {
                resolution_note,
                resolved_by,
            } => {
                let alert_resolved = match find_open_alert(&mut tx, &reading.asset.asset_id).await?
                {
                    Some(open) => Some(
                        resolve_alert(
                            &mut tx,
                            open.alert_id,
                            reading.observed_at,
                            &resolution_note,
                            &resolved_by,
                        )
                        .await?,
                    ),
                    None => None,
                };
                IngestWrite {
                    alert_opened: None,
                    alert_resolved,
                }
            }
            PolicyOutcome::Unchanged => IngestWrite::default(),
        };

        tx.commit().await.map_err(PortError::storage)?;

        Ok(write)
    }
}

async fn upsert_asset(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
) -> Result<(), PortError> {
    query(
        r#"
        INSERT INTO assets (
            asset_id, site_id, asset_type, internal_temperature, last_seen_at
        )
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (asset_id) DO UPDATE SET
            site_id = EXCLUDED.site_id,
            asset_type = EXCLUDED.asset_type,
            internal_temperature = EXCLUDED.internal_temperature,
            last_seen_at = EXCLUDED.last_seen_at
        "#,
    )
    .bind(&reading.asset.asset_id)
    .bind(&reading.asset.site_id)
    .bind(reading.asset.asset_type.as_str())
    .bind(reading.asset.internal_temperature)
    .bind(reading.asset.last_seen_at)
    .execute(&mut **tx)
    .await
    .map_err(PortError::storage)?;

    Ok(())
}

async fn upsert_state(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
) -> Result<(), PortError> {
    match &reading.state {
        AssetState::SmartMeter(state) => {
            query(
                r#"
                INSERT INTO smart_meter_state (
                    asset_id, reported_household_id, relay_closed, voltage, current,
                    active_power, frequency, total_kwh
                )
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
                ON CONFLICT (asset_id) DO UPDATE SET
                    reported_household_id = EXCLUDED.reported_household_id,
                    relay_closed = EXCLUDED.relay_closed,
                    voltage = EXCLUDED.voltage,
                    current = EXCLUDED.current,
                    active_power = EXCLUDED.active_power,
                    frequency = EXCLUDED.frequency,
                    total_kwh = EXCLUDED.total_kwh
                "#,
            )
            .bind(&reading.asset.asset_id)
            .bind(&state.reported_household_id)
            .bind(state.relay_closed)
            .bind(state.voltage)
            .bind(state.current)
            .bind(state.active_power)
            .bind(state.frequency)
            .bind(state.total_kwh)
            .execute(&mut **tx)
            .await
            .map_err(PortError::storage)?;
        }
        AssetState::BatteryBms(state) => {
            query(
                r#"
                INSERT INTO battery_bms_state (asset_id, battery_soc_pct)
                VALUES ($1, $2)
                ON CONFLICT (asset_id) DO UPDATE SET
                    battery_soc_pct = EXCLUDED.battery_soc_pct
                "#,
            )
            .bind(&reading.asset.asset_id)
            .bind(state.battery_soc_pct)
            .execute(&mut **tx)
            .await
            .map_err(PortError::storage)?;
        }
        AssetState::SolarInverter(state) => {
            query(
                r#"
                INSERT INTO solar_inverter_state (asset_id, solar_irradiance)
                VALUES ($1, $2)
                ON CONFLICT (asset_id) DO UPDATE SET
                    solar_irradiance = EXCLUDED.solar_irradiance
                "#,
            )
            .bind(&reading.asset.asset_id)
            .bind(state.solar_irradiance)
            .execute(&mut **tx)
            .await
            .map_err(PortError::storage)?;
        }
    }

    Ok(())
}

async fn append_reading(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
) -> Result<(), PortError> {
    match &reading.state {
        AssetState::SmartMeter(state) => {
            query(
                r#"
                INSERT INTO smart_meter_readings (
                    time, asset_id, internal_temperature, reported_household_id,
                    relay_closed, voltage, current, active_power, frequency, total_kwh
                )
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
                "#,
            )
            .bind(reading.observed_at)
            .bind(&reading.asset.asset_id)
            .bind(reading.asset.internal_temperature)
            .bind(&state.reported_household_id)
            .bind(state.relay_closed)
            .bind(state.voltage)
            .bind(state.current)
            .bind(state.active_power)
            .bind(state.frequency)
            .bind(state.total_kwh)
            .execute(&mut **tx)
            .await
            .map_err(PortError::storage)?;
        }
        AssetState::BatteryBms(state) => {
            query(
                r#"
                INSERT INTO battery_bms_readings (
                    time, asset_id, internal_temperature, battery_soc_pct
                )
                VALUES ($1, $2, $3, $4)
                "#,
            )
            .bind(reading.observed_at)
            .bind(&reading.asset.asset_id)
            .bind(reading.asset.internal_temperature)
            .bind(state.battery_soc_pct)
            .execute(&mut **tx)
            .await
            .map_err(PortError::storage)?;
        }
        AssetState::SolarInverter(state) => {
            query(
                r#"
                INSERT INTO solar_inverter_readings (
                    time, asset_id, internal_temperature, solar_irradiance
                )
                VALUES ($1, $2, $3, $4)
                "#,
            )
            .bind(reading.observed_at)
            .bind(&reading.asset.asset_id)
            .bind(reading.asset.internal_temperature)
            .bind(state.solar_irradiance)
            .execute(&mut **tx)
            .await
            .map_err(PortError::storage)?;
        }
    }

    Ok(())
}

async fn open_alert(
    tx: &mut Transaction<'_, Postgres>,
    decision: &AlertDecision,
) -> Result<Alert, PortError> {
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
    .fetch_one(&mut **tx)
    .await
    .map_err(PortError::storage)?;

    row_to_alert(row)
}

async fn find_open_alert(
    tx: &mut Transaction<'_, Postgres>,
    asset_id: &str,
) -> Result<Option<Alert>, PortError> {
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
    .fetch_optional(&mut **tx)
    .await
    .map_err(PortError::storage)?;

    row.map(row_to_alert).transpose()
}

async fn resolve_alert(
    tx: &mut Transaction<'_, Postgres>,
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
    .fetch_optional(&mut **tx)
    .await
    .map_err(PortError::storage)?
    .ok_or_else(|| PortError::message(format!("alert {alert_id} is not open")))?;

    row_to_alert(row)
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
