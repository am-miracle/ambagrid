use chrono::Utc;
use sqlx::{PgPool, Row, postgres::PgPoolOptions, query};
use uuid::Uuid;

use crate::{
    MIGRATOR,
    adapters::postgres::PostgresIngestRepository,
    domain::asset::{Asset, AssetState, Reading, SmartMeterState},
    ports::{IngestRepository, PolicyOutcome},
};

#[tokio::test]
#[ignore = "requires DATABASE_URL and a writable Postgres database"]
async fn redelivered_reading_with_the_same_source_event_id_is_not_duplicated() {
    let pool = migrated_pool().await;
    let suffix = Uuid::new_v4().simple().to_string();
    let site_id = format!("it-dedup-site-{suffix}");
    let asset_id = format!("it-dedup-meter-{suffix}");
    let event_id = format!("edge:{site_id}:1");

    seed_site(&pool, &site_id).await;

    let repository =
        PostgresIngestRepository::with_alert_topics(pool.clone(), "alert.opened", "alert.resolved");
    let reading = reading(&site_id, &asset_id, &event_id);

    repository
        .ingest(&reading, PolicyOutcome::Unchanged)
        .await
        .unwrap();
    repository
        .ingest(&reading, PolicyOutcome::Unchanged)
        .await
        .unwrap();

    let reading_count: i64 =
        query("SELECT count(*) AS n FROM smart_meter_readings WHERE asset_id = $1")
            .bind(&asset_id)
            .fetch_one(&pool)
            .await
            .unwrap()
            .get("n");

    assert_eq!(
        reading_count, 1,
        "a redelivered reading with the same source_event_id must not insert a second row"
    );

    cleanup(&pool, &site_id, &asset_id, &event_id).await;
}

fn reading(site_id: &str, asset_id: &str, event_id: &str) -> Reading {
    Reading {
        asset: Asset {
            asset_id: asset_id.to_string(),
            site_id: site_id.to_string(),
            internal_temperature: Some(30.0),
            last_seen_at: Utc::now(),
        },
        observed_at: Utc::now(),
        source_event_id: Some(event_id.to_string()),
        state: AssetState::SmartMeter(SmartMeterState {
            reported_household_id: None,
            relay_closed: Some(true),
            voltage: Some(230.0),
            current: Some(1.5),
            active_power: Some(345.0),
            frequency: Some(50.0),
            total_kwh: Some(12.5),
        }),
        edge: None,
    }
}

async fn seed_site(pool: &PgPool, site_id: &str) {
    query("INSERT INTO sites (site_id, name) VALUES ($1, 'Dedup Integration Site')")
        .bind(site_id)
        .execute(pool)
        .await
        .unwrap();
}

async fn cleanup(pool: &PgPool, site_id: &str, asset_id: &str, event_id: &str) {
    query("DELETE FROM dedup_log WHERE source_event_id = $1")
        .bind(event_id)
        .execute(pool)
        .await
        .unwrap();
    query("DELETE FROM smart_meter_readings WHERE asset_id = $1")
        .bind(asset_id)
        .execute(pool)
        .await
        .unwrap();
    query("DELETE FROM smart_meter_state WHERE asset_id = $1")
        .bind(asset_id)
        .execute(pool)
        .await
        .unwrap();
    query("DELETE FROM assets WHERE asset_id = $1")
        .bind(asset_id)
        .execute(pool)
        .await
        .unwrap();
    query("DELETE FROM sites WHERE site_id = $1")
        .bind(site_id)
        .execute(pool)
        .await
        .unwrap();
}

async fn migrated_pool() -> PgPool {
    let database_url =
        std::env::var("DATABASE_URL").expect("DATABASE_URL must be set for ignored test");
    let pool = PgPoolOptions::new()
        .max_connections(1)
        .connect(&database_url)
        .await
        .unwrap();
    MIGRATOR.run(&pool).await.unwrap();
    pool
}
