use std::time::Duration;

use chrono::{DateTime, Utc};
use rdkafka::{
    Message,
    consumer::{Consumer, StreamConsumer},
};
use serde::Deserialize;
use sqlx::{PgPool, query};

use super::client_config;
use crate::config::KafkaSecurity;

#[derive(Debug, Clone)]
pub struct SiteHealthConsumerConfig {
    pub brokers: Vec<String>,
    pub security: KafkaSecurity,
    pub topic: String,
    pub group_id: String,
}

#[derive(Debug, Deserialize)]
struct SiteHealthEvent {
    site_id: String,
    gateway_id: String,
    contact_at: DateTime<Utc>,
    last_event_timestamp: Option<DateTime<Utc>>,
    queue_depth: i64,
    oldest_pending_at: Option<DateTime<Utc>>,
    #[serde(default)]
    last_successful_sync_at: Option<DateTime<Utc>>,
    #[serde(default)]
    last_failed_sync_reason: Option<String>,
}

pub async fn run(
    config: SiteHealthConsumerConfig,
    pool: PgPool,
) -> Result<(), Box<dyn std::error::Error>> {
    let consumer: StreamConsumer = client_config(&config.brokers, &config.security)
        .set("group.id", &config.group_id)
        .set("auto.offset.reset", "earliest")
        .set("enable.auto.commit", "true")
        .set("enable.auto.offset.store", "false")
        .create()?;
    consumer.subscribe(&[&config.topic])?;

    loop {
        let message = match consumer.recv().await {
            Ok(message) => message,
            Err(err) => {
                tracing::error!(error = %err, "site health receive failed; retrying");
                tokio::time::sleep(Duration::from_millis(500)).await;
                continue;
            }
        };
        let Some(payload) = message.payload() else {
            let _ = consumer.store_offset_from_message(&message);
            continue;
        };
        let event: SiteHealthEvent = match serde_json::from_slice(payload) {
            Ok(event) => event,
            Err(err) => {
                tracing::warn!(error = %err, "discarding malformed site health event");
                let _ = consumer.store_offset_from_message(&message);
                continue;
            }
        };
        if let Err(err) = store(&pool, &event).await {
            tracing::error!(error = %err, site_id = %event.site_id, "store site health failed; leaving offset unstored");
            tokio::time::sleep(Duration::from_millis(500)).await;
            continue;
        }
        if let Err(err) = consumer.store_offset_from_message(&message) {
            tracing::error!(error = %err, site_id = %event.site_id, "store site health offset failed");
        }
    }
}

async fn store(pool: &PgPool, event: &SiteHealthEvent) -> Result<(), sqlx::Error> {
    query(
        r#"
        INSERT INTO site_health (
            site_id, gateway_id, last_contact_at, last_event_timestamp, queue_depth,
            oldest_pending_at, queue_growing, last_successful_sync_at, last_failed_sync_reason
        ) VALUES ($1, $2, $3, $4, $5, $6, false, $7, $8)
        ON CONFLICT (site_id) DO UPDATE SET
            gateway_id = EXCLUDED.gateway_id,
            last_contact_at = EXCLUDED.last_contact_at,
            last_event_timestamp = EXCLUDED.last_event_timestamp,
            queue_growing = EXCLUDED.queue_depth > site_health.queue_depth,
            queue_depth = EXCLUDED.queue_depth,
            oldest_pending_at = EXCLUDED.oldest_pending_at,
            last_successful_sync_at = EXCLUDED.last_successful_sync_at,
            last_failed_sync_reason = EXCLUDED.last_failed_sync_reason
        "#,
    )
    .bind(&event.site_id)
    .bind(&event.gateway_id)
    .bind(event.contact_at)
    .bind(event.last_event_timestamp)
    .bind(event.queue_depth)
    .bind(event.oldest_pending_at)
    .bind(event.last_successful_sync_at)
    .bind(&event.last_failed_sync_reason)
    .execute(pool)
    .await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_health_events_from_before_sync_indicators() {
        let event: SiteHealthEvent = serde_json::from_str(
            r#"{"site_id":"site-01","gateway_id":"gw-01","contact_at":"2026-10-14T10:00:00Z","last_event_timestamp":null,"queue_depth":0,"oldest_pending_at":null}"#,
        )
        .unwrap();
        assert_eq!(event.site_id, "site-01");
        assert!(event.last_successful_sync_at.is_none());
        assert!(event.last_failed_sync_reason.is_none());
    }
}
