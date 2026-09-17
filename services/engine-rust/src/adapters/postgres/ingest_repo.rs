use std::str::FromStr;

use chrono::{DateTime, Utc};
use serde::Serialize;
use sqlx::{PgPool, Postgres, Row, Transaction, postgres::PgRow, query};
use uuid::Uuid;

use crate::{
    alert_outbox::{AlertOpenedPayload, AlertResolvedPayload},
    domain::{
        actor::{ActorId, ResolutionActor},
        alert::{Alert, AlertDecision, AlertKind},
        asset::{AssetState, BatteryBmsState, Reading, SmartMeterState, SolarInverterState},
    },
    ports::{
        AlertRepository, IngestRepository, IngestWrite, OutboxEventType, PolicyOutcome, PortError,
    },
};

#[derive(Debug, Clone)]
pub struct PostgresIngestRepository {
    pool: PgPool,
    alert_opened_topic: String,
    alert_resolved_topic: String,
}

impl PostgresIngestRepository {
    pub fn with_alert_topics(
        pool: PgPool,
        alert_opened_topic: impl Into<String>,
        alert_resolved_topic: impl Into<String>,
    ) -> Self {
        Self {
            pool,
            alert_opened_topic: alert_opened_topic.into(),
            alert_resolved_topic: alert_resolved_topic.into(),
        }
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
                        Some(open_alert(&mut tx, &decision, &self.alert_opened_topic).await?)
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
                                &self.alert_resolved_topic,
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
        resolved_by: ResolutionActor,
    ) -> Result<Alert, PortError> {
        let mut tx = self.pool.begin().await.map_err(PortError::storage)?;
        let alert = resolve_alert(
            &mut tx,
            alert_id,
            resolved_at,
            resolution_note,
            &resolved_by,
            &self.alert_resolved_topic,
        )
        .await?;
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

// Every asset type follows the same two-step protocol — overwrite the current
// state row, then append an immutable reading — but each has its own tables
// and column list, so the SQL is written out per type rather than generated.
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
    topic: &str,
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

    let alert = row_to_alert(row)?;
    insert_alert_event(
        tx,
        topic,
        OutboxEventType::AlertOpened,
        alert.alert_id,
        alert_opened_payload(&alert)?,
    )
    .await?;
    Ok(alert)
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
    resolved_by: &ResolutionActor,
    topic: &str,
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
    .bind(resolved_by.as_str())
    .fetch_optional(&mut **tx)
    .await
    .map_err(PortError::storage)?
    .ok_or_else(|| PortError::message(format!("alert {alert_id} is not open")))?;

    insert_alert_resolution_history(tx, alert_id, resolved_at, resolution_note, resolved_by)
        .await?;

    let alert = row_to_alert(row)?;
    insert_alert_event(
        tx,
        topic,
        OutboxEventType::AlertResolved,
        alert.alert_id,
        alert_resolved_payload(&alert)?,
    )
    .await?;
    Ok(alert)
}

async fn insert_alert_event(
    tx: &mut Transaction<'_, Postgres>,
    topic: &str,
    event_type: OutboxEventType,
    alert_id: Uuid,
    payload: impl Serialize,
) -> Result<(), PortError> {
    let payload = serde_json::to_string(&payload).map_err(PortError::storage)?;

    query(
        r#"
        INSERT INTO command_event_outbox (
            topic, event_type, aggregate_type, aggregate_id, payload
        )
        VALUES ($1, $2, 'alert', $3, $4::jsonb)
        "#,
    )
    .bind(topic)
    .bind(event_type.as_str())
    .bind(alert_id)
    .bind(payload)
    .execute(&mut **tx)
    .await
    .map_err(PortError::storage)?;

    Ok(())
}

fn alert_opened_payload(alert: &Alert) -> Result<AlertOpenedPayload, PortError> {
    let asset_id = required_alert_text(alert.alert_id, "asset_id", &alert.asset_id)?;
    let site_id = required_alert_text(alert.alert_id, "site_id", &alert.site_id)?;
    let reason = required_alert_text(alert.alert_id, "reason", &alert.reason)?;

    Ok(AlertOpenedPayload {
        alert_id: alert.alert_id.to_string(),
        asset_id: asset_id.to_string(),
        site_id: site_id.to_string(),
        severity: alert.severity.into(),
        reason: reason.to_string(),
        opened_at_utc: alert.opened_at.timestamp(),
        source_event_id: alert.source_event_id.clone(),
    })
}

fn alert_resolved_payload(alert: &Alert) -> Result<AlertResolvedPayload, PortError> {
    let resolved_at = alert.resolved_at.ok_or_else(|| {
        PortError::message(format!(
            "resolved alert {} is missing resolved_at",
            alert.alert_id
        ))
    })?;
    let resolution_note = alert.resolution_note.as_deref().ok_or_else(|| {
        PortError::message(format!(
            "resolved alert {} is missing resolution_note",
            alert.alert_id
        ))
    })?;
    let resolved_by = alert.resolved_by.as_ref().ok_or_else(|| {
        PortError::message(format!(
            "resolved alert {} is missing resolved_by",
            alert.alert_id
        ))
    })?;

    Ok(AlertResolvedPayload {
        alert_id: alert.alert_id.to_string(),
        asset_id: alert.asset_id.clone(),
        site_id: alert.site_id.clone(),
        severity: alert.severity.into(),
        reason: alert.reason.clone(),
        opened_at_utc: alert.opened_at.timestamp(),
        resolved_at_utc: resolved_at.timestamp(),
        resolution_note: resolution_note.to_string(),
        resolved_by: resolved_by.as_str().to_string(),
    })
}

fn required_alert_text<'a>(
    alert_id: Uuid,
    field: &'static str,
    value: &'a str,
) -> Result<&'a str, PortError> {
    if value.trim().is_empty() {
        return Err(PortError::message(format!(
            "alert {alert_id} is missing {field}"
        )));
    }
    Ok(value)
}

async fn insert_alert_resolution_history(
    tx: &mut Transaction<'_, Postgres>,
    alert_id: Uuid,
    resolved_at: DateTime<Utc>,
    resolution_note: &str,
    resolved_by: &ResolutionActor,
) -> Result<(), PortError> {
    query(
        r#"
        INSERT INTO alert_resolution_history (
            alert_id, resolved_at, resolution_note, resolved_by
        )
        VALUES ($1, $2, $3, $4)
        "#,
    )
    .bind(alert_id)
    .bind(resolved_at)
    .bind(resolution_note)
    .bind(resolved_by.as_str())
    .execute(&mut **tx)
    .await
    .map_err(PortError::storage)?;

    Ok(())
}

fn row_to_alert(row: PgRow) -> Result<Alert, PortError> {
    Ok(Alert {
        alert_id: row.get::<Uuid, _>("alert_id"),
        asset_id: row.get("asset_id"),
        site_id: row.get("site_id"),
        kind: AlertKind::from(row.get::<String, _>("kind")),
        severity: parse_domain_value(row.get::<String, _>("severity").as_str())?,
        status: parse_domain_value(row.get::<String, _>("status").as_str())?,
        reason: row.get("reason"),
        opened_at: row.get::<DateTime<Utc>, _>("opened_at"),
        source_event_id: row.get("source_event_id"),
        resolved_at: row.get::<Option<DateTime<Utc>>, _>("resolved_at"),
        resolution_note: row.get("resolution_note"),
        resolved_by: row
            .get::<Option<String>, _>("resolved_by")
            .map(parse_resolution_actor)
            .transpose()?,
    })
}

fn parse_resolution_actor(value: String) -> Result<ResolutionActor, PortError> {
    if value == "system" {
        Ok(ResolutionActor::System)
    } else {
        ActorId::new(value)
            .map(ResolutionActor::Human)
            .map_err(|err| PortError::message(err.to_string()))
    }
}

// A value the database holds that the domain no longer recognises is a
// schema-drift bug, not a transient fault, so it maps to a non-retryable
// PortError::Message.
fn parse_domain_value<T>(value: &str) -> Result<T, PortError>
where
    T: FromStr,
    T::Err: std::fmt::Display,
{
    T::from_str(value).map_err(|err| PortError::message(err.to_string()))
}

#[cfg(test)]
mod tests {
    use chrono::TimeZone;

    use crate::domain::alert::{AlertStatus, Severity};

    use super::*;

    fn alert() -> Alert {
        Alert {
            alert_id: Uuid::parse_str("2f1b98a9-7ab4-4bb7-8f24-90ae8b09a76c").unwrap(),
            asset_id: "met-0101".to_string(),
            site_id: "ng-kaji-01".to_string(),
            kind: AlertKind::from("internal_temperature"),
            severity: Severity::Critical,
            status: AlertStatus::Open,
            reason: "internal_temperature_high".to_string(),
            opened_at: Utc.timestamp_opt(1_789_351_200, 0).unwrap(),
            source_event_id: Some("telemetry.ingested:2:17".to_string()),
            resolved_at: None,
            resolution_note: None,
            resolved_by: None,
        }
    }

    #[test]
    fn alert_opened_outbox_payload_matches_the_protobuf_json_shape() {
        let payload = alert_opened_payload(&alert()).unwrap();
        let payload_json = serde_json::to_value(&payload).unwrap();
        let fixture: serde_json::Value = serde_json::from_slice(include_bytes!(
            "../../../../../proto/fixtures/alert_opened_outbox_payload.json"
        ))
        .unwrap();

        assert_eq!(payload_json, fixture);
        assert_eq!(
            payload.severity,
            crate::alert_outbox::AlertSeverity::Critical
        );
        assert_eq!(payload.opened_at_utc, 1_789_351_200);
        assert_eq!(
            payload.source_event_id.as_deref(),
            Some("telemetry.ingested:2:17")
        );
    }

    #[test]
    fn alert_opened_outbox_payload_rejects_missing_required_text() {
        for (field, malformed) in [
            (
                "asset_id",
                Alert {
                    asset_id: " ".to_string(),
                    ..alert()
                },
            ),
            (
                "site_id",
                Alert {
                    site_id: "".to_string(),
                    ..alert()
                },
            ),
            (
                "reason",
                Alert {
                    reason: "\t".to_string(),
                    ..alert()
                },
            ),
        ] {
            let err = alert_opened_payload(&malformed).unwrap_err();
            assert!(
                err.to_string().contains(field),
                "expected {err} to mention {field}"
            );
        }
    }

    #[test]
    fn alert_resolved_outbox_payload_preserves_resolution_fields() {
        let opened = alert();
        let resolved = Alert {
            status: AlertStatus::Resolved,
            resolved_at: Some(Utc.timestamp_opt(1_789_351_500, 0).unwrap()),
            resolution_note: Some("temperature recovered".to_string()),
            resolved_by: Some(ResolutionActor::System),
            ..opened
        };

        let payload = alert_resolved_payload(&resolved).unwrap();

        assert_eq!(payload.resolved_at_utc, 1_789_351_500);
        assert_eq!(payload.resolution_note, "temperature recovered");
        assert_eq!(payload.resolved_by, "system");
    }
}
