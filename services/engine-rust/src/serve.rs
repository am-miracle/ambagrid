use std::time::Duration;

use sqlx::postgres::PgPoolOptions;

use crate::{
    actions::ingest_reading::IngestReading,
    adapters::{
        kafka::consumer::{self, ConsumerConfig},
        postgres::{
            PostgresAlertRepository, PostgresAssetRepository, PostgresDeadLetterSink,
            PostgresReadingsRepository,
        },
    },
    config::Config,
};

pub async fn run() -> Result<(), Box<dyn std::error::Error>> {
    let cfg = Config::from_env()?;
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .acquire_timeout(Duration::from_secs(2))
        .connect(&cfg.database_url)
        .await?;

    let assets = PostgresAssetRepository::new(pool.clone());
    let readings = PostgresReadingsRepository::new(pool.clone());
    let alerts = PostgresAlertRepository::new(pool.clone());
    let dead_letters = PostgresDeadLetterSink::new(pool);
    let ingest = IngestReading::new(&assets, &readings, &alerts);

    consumer::run(
        ConsumerConfig {
            brokers: cfg.kafka_brokers,
            topic: cfg.telemetry_topic,
        },
        &ingest,
        &dead_letters,
    )
    .await?;

    Ok(())
}
