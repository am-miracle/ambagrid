use chrono::{DateTime, Utc};
use sqlx::{PgPool, Postgres, Row, Transaction, postgres::PgRow, query};
use uuid::Uuid;

use crate::{
    domain::{
        alert::{Alert, AlertDecision, AlertKind, AlertStatus, Severity},
        asset::{AssetState, BatteryBmsState, Reading, SmartMeterState, SolarInverterState},
    },
    ports::{AlertRepository, IngestRepository, IngestWrite, PolicyOutcome, PortError},
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
        write_asset_state_and_reading(&mut tx, reading).await?;

        let write = match outcome {
            PolicyOutcome::Open(decision) => {
                let alert_opened =
                    if find_open_alert(&mut tx, &decision.asset_id, decision.kind.as_str())
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
                kind,
                resolution_note,
                resolved_by,
            } => {
                let alert_resolved =
                    match find_open_alert(&mut tx, &reading.asset.asset_id, kind.as_str()).await? {
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

impl AlertRepository for PostgresIngestRepository {
    async fn resolve_alert(
        &self,
        alert_id: Uuid,
        resolved_at: DateTime<Utc>,
        resolution_note: &str,
        resolved_by: &str,
    ) -> Result<Alert, PortError> {
        let mut tx = self.pool.begin().await.map_err(PortError::storage)?;
        let alert =
            resolve_alert(&mut tx, alert_id, resolved_at, resolution_note, resolved_by).await?;
        tx.commit().await.map_err(PortError::storage)?;

        Ok(alert)
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
    .bind(reading.asset_type().as_str())
    .bind(reading.asset.internal_temperature)
    .bind(reading.asset.last_seen_at)
    .execute(&mut **tx)
    .await
    .map_err(PortError::storage)?;

    Ok(())
}

async fn write_asset_state_and_reading(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
) -> Result<(), PortError> {
    match &reading.state {
        AssetState::SmartMeter(state) => {
            upsert_smart_meter_state(tx, reading, state).await?;
            append_smart_meter_reading(tx, reading, state).await?;
        }
        AssetState::BatteryBms(state) => {
            upsert_battery_bms_state(tx, reading, state).await?;
            append_battery_bms_reading(tx, reading, state).await?;
        }
        AssetState::SolarInverter(state) => {
            upsert_solar_inverter_state(tx, reading, state).await?;
            append_solar_inverter_reading(tx, reading, state).await?;
        }
    }

    Ok(())
}

async fn upsert_smart_meter_state(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
    state: &SmartMeterState,
) -> Result<(), PortError> {
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

    Ok(())
}

async fn append_smart_meter_reading(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
    state: &SmartMeterState,
) -> Result<(), PortError> {
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

    Ok(())
}

async fn upsert_battery_bms_state(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
    state: &BatteryBmsState,
) -> Result<(), PortError> {
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

    Ok(())
}

async fn append_battery_bms_reading(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
    state: &BatteryBmsState,
) -> Result<(), PortError> {
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

    Ok(())
}

async fn upsert_solar_inverter_state(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
    state: &SolarInverterState,
) -> Result<(), PortError> {
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

    Ok(())
}

async fn append_solar_inverter_reading(
    tx: &mut Transaction<'_, Postgres>,
    reading: &Reading,
    state: &SolarInverterState,
) -> Result<(), PortError> {
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

    Ok(())
}

async fn open_alert(
    tx: &mut Transaction<'_, Postgres>,
    decision: &AlertDecision,
) -> Result<Alert, PortError> {
    let row = query(
        r#"
        INSERT INTO alerts (
            asset_id, site_id, kind, severity, status, reason, opened_at, source_event_id
        )
        VALUES ($1, $2, $3, $4, 'open', $5, $6, $7)
        RETURNING alert_id, asset_id, site_id, kind, severity, status, reason, opened_at,
            source_event_id, resolved_at, resolution_note, resolved_by
        "#,
    )
    .bind(&decision.asset_id)
    .bind(&decision.site_id)
    .bind(decision.kind.as_str())
    .bind(decision.severity.as_str())
    .bind(&decision.reason)
    .bind(decision.opened_at)
    .bind(&decision.source_event_id)
    .fetch_one(&mut **tx)
    .await
    .map_err(PortError::storage)?;

    row_to_alert(row)
}

// Scoped by kind, not just asset_id: an asset can have multiple concurrent
// open alerts for distinct problems (e.g. overheating and low battery), and
// a recovery reading for one kind must not resolve another.
async fn find_open_alert(
    tx: &mut Transaction<'_, Postgres>,
    asset_id: &str,
    kind: &str,
) -> Result<Option<Alert>, PortError> {
    let row = query(
        r#"
        SELECT alert_id, asset_id, site_id, kind, severity, status, reason, opened_at, source_event_id,
            resolved_at, resolution_note, resolved_by
        FROM alerts
        WHERE asset_id = $1 AND kind = $2 AND status = 'open'
        ORDER BY opened_at DESC
        LIMIT 1
        "#,
    )
    .bind(asset_id)
    .bind(kind)
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
        RETURNING alert_id, asset_id, site_id, kind, severity, status, reason, opened_at, source_event_id,
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
        kind: AlertKind::from(row.get::<String, _>("kind")),
        severity: parse_severity(row.get::<String, _>("severity").as_str())?,
        status: parse_status(row.get::<String, _>("status").as_str())?,
        reason: row.get("reason"),
        opened_at: row.get::<DateTime<Utc>, _>("opened_at"),
        source_event_id: row.get("source_event_id"),
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
