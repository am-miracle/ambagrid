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

const UPSERT_SITE_HEALTH: &str = r#"
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
    WHERE EXCLUDED.last_contact_at > site_health.last_contact_at
"#;

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
    // The consumer and database commit independently, so Kafka may redeliver a
    // health event after it has already been stored. The timestamp predicate in
    // the upsert keeps duplicates and stale replays from regressing health or
    // recalculating queue_growing against a newer depth.
    query(UPSERT_SITE_HEALTH)
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
    use sqlx::{Row, postgres::PgPoolOptions, query};
    use uuid::Uuid;

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

    #[tokio::test]
    #[ignore = "requires DATABASE_URL and a writable Postgres database"]
    async fn stale_and_duplicate_events_do_not_replace_newer_health() {
        let database_url =
            std::env::var("DATABASE_URL").expect("DATABASE_URL must be set for ignored test");
        let pool = PgPoolOptions::new()
            .max_connections(1)
            .connect(&database_url)
            .await
            .unwrap();
        sqlx::migrate!().run(&pool).await.unwrap();

        let site_id = format!("it-health-{}", Uuid::new_v4().simple());
        query("INSERT INTO sites (site_id, name) VALUES ($1, $1)")
            .bind(&site_id)
            .execute(&pool)
            .await
            .unwrap();

        let first_at = "2026-10-20T10:00:00Z".parse().unwrap();
        let latest_at = "2026-10-20T10:00:05Z".parse().unwrap();
        let stale_at = "2026-10-20T09:59:55Z".parse().unwrap();

        store(&pool, &health_event(&site_id, "gw-current", first_at, 2))
            .await
            .unwrap();
        store(&pool, &health_event(&site_id, "gw-current", latest_at, 5))
            .await
            .unwrap();

        // A redelivery must preserve the growth result from the transition
        // between the first and latest observations.
        store(&pool, &health_event(&site_id, "gw-duplicate", latest_at, 0))
            .await
            .unwrap();
        store(&pool, &health_event(&site_id, "gw-stale", stale_at, 9))
            .await
            .unwrap();

        let row = query(
            "SELECT gateway_id, last_contact_at, queue_depth, queue_growing FROM site_health WHERE site_id = $1",
        )
        .bind(&site_id)
        .fetch_one(&pool)
        .await
        .unwrap();
        assert_eq!(row.get::<String, _>("gateway_id"), "gw-current");
        assert_eq!(row.get::<DateTime<Utc>, _>("last_contact_at"), latest_at);
        assert_eq!(row.get::<i64, _>("queue_depth"), 5);
        assert!(row.get::<bool, _>("queue_growing"));

        query("DELETE FROM sites WHERE site_id = $1")
            .bind(&site_id)
            .execute(&pool)
            .await
            .unwrap();
    }

    fn health_event(
        site_id: &str,
        gateway_id: &str,
        contact_at: DateTime<Utc>,
        queue_depth: i64,
    ) -> SiteHealthEvent {
        SiteHealthEvent {
            site_id: site_id.to_owned(),
            gateway_id: gateway_id.to_owned(),
            contact_at,
            last_event_timestamp: Some(contact_at),
            queue_depth,
            oldest_pending_at: (queue_depth > 0).then_some(contact_at),
            last_successful_sync_at: Some(contact_at),
            last_failed_sync_reason: None,
        }
    }
}
