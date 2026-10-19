use std::time::Duration;

use sqlx::postgres::PgPoolOptions;

use crate::{
    actions::ingest_reading::IngestReading,
    adapters::{
        kafka::{
            consumer::{self, ConsumerConfig},
            producer::KafkaDeadLetterPublisher,
            site_health::{self, SiteHealthConsumerConfig},
        },
        postgres::PostgresIngestRepository,
    },
    config::Config,
    metrics::Metrics,
};

pub async fn run() -> Result<(), Box<dyn std::error::Error>> {
    let cfg = Config::from_env()?;
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .acquire_timeout(Duration::from_secs(2))
        .connect(&cfg.database_url)
        .await?;

    let store = PostgresIngestRepository::with_alert_topics(
        pool.clone(),
        cfg.alert_opened_topic.clone(),
        cfg.alert_resolved_topic.clone(),
    );
    let dead_letters = KafkaDeadLetterPublisher::connect(
        &cfg.kafka_brokers,
        &cfg.kafka_security,
        cfg.telemetry_dlq_topic,
        Metrics::new()?,
    )?;
    let mut ingest = IngestReading::new(&store);
    ingest.policy = cfg.threshold_policy;

    let telemetry = consumer::run(
        ConsumerConfig {
            brokers: cfg.kafka_brokers.clone(),
            security: cfg.kafka_security.clone(),
            topic: cfg.telemetry_topic,
            group_id: cfg.telemetry_group_id,
        },
        &ingest,
        &dead_letters,
    );
    let health = site_health::run(
        SiteHealthConsumerConfig {
            brokers: cfg.kafka_brokers,
            security: cfg.kafka_security,
            topic: cfg.site_health_topic,
            group_id: cfg.site_health_group_id,
        },
        pool,
    );
    tokio::select! {
        result = telemetry => result.map_err(Into::into),
        result = health => result,
    }
}
