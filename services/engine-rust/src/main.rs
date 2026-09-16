mod actions;
mod adapters;
mod config;
mod domain;
mod metrics;
mod ports;
mod serve;
mod telemetry;

use std::time::Duration;

use actions::{
    publish_outbox::PublishOutbox,
    resolve_alert::{AllowedResolveActors, ResolveAlert, ResolveAlertInput},
};
use adapters::{
    kafka::producer::KafkaOutboxEventPublisher,
    postgres::{PostgresIngestRepository, PostgresOutboxRepository},
};
use config::Config;
use domain::actor::ActorId;
use sqlx::postgres::PgPoolOptions;
use tracing_subscriber::EnvFilter;
use uuid::Uuid;

static MIGRATOR: sqlx::migrate::Migrator = sqlx::migrate!();

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    init_logging();

    let command = std::env::args()
        .nth(1)
        .unwrap_or_else(|| "serve".to_string());

    match command.as_str() {
        "migrate" => run_migrations().await,
        "serve" => serve().await,
        "publish-outbox" => publish_outbox().await,
        "resolve-alert" => resolve_alert(std::env::args().skip(2).collect()).await,
        "-h" | "--help" | "help" => {
            print_usage();
            Ok(())
        }
        _ => {
            print_usage();
            Err(format!("unknown command: {command}").into())
        }
    }
}

async fn resolve_alert(args: Vec<String>) -> Result<(), Box<dyn std::error::Error>> {
    if args.len() < 3 {
        print_usage();
        return Err("resolve-alert requires: <alert_id> <resolved_by> <resolution_note>".into());
    }

    let alert_id = Uuid::parse_str(&args[0])?;
    let resolved_by = ActorId::new(args[1].clone())?;
    let resolution_note = args[2..].join(" ");

    let cfg = Config::from_env()?;
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .connect(&cfg.database_url)
        .await?;
    let store = PostgresIngestRepository::with_alert_topics(
        pool,
        cfg.alert_opened_topic.clone(),
        cfg.alert_resolved_topic.clone(),
    );
    let permissions = AllowedResolveActors::new(cfg.alert_resolve_actors.clone());
    let action = ResolveAlert::new(&store, &permissions);

    let alert = action
        .execute(ResolveAlertInput {
            alert_id,
            resolution_note,
            resolved_by,
        })
        .await?;

    tracing::info!(alert_id = %alert.alert_id, "alert resolved");
    Ok(())
}

async fn publish_outbox() -> Result<(), Box<dyn std::error::Error>> {
    const BATCH_SIZE: i64 = 50;
    const IDLE_DELAY: Duration = Duration::from_secs(1);
    const FULL_BATCH_DELAY: Duration = Duration::from_millis(10);
    const MAX_FAILURE_DELAY: Duration = Duration::from_secs(30);
    const CLEANUP_INTERVAL: Duration = Duration::from_secs(60 * 60);
    const PUBLISHED_RETENTION: Duration = Duration::from_secs(30 * 24 * 60 * 60);

    let cfg = Config::from_env()?;
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .connect(&cfg.database_url)
        .await?;
    let store = PostgresOutboxRepository::new(pool);
    let events = KafkaOutboxEventPublisher::connect(cfg.kafka_brokers)?;
    let publisher = PublishOutbox::new(&store, &events, BATCH_SIZE);
    let mut consecutive_failures = 0;
    let mut cleanup = tokio::time::interval(CLEANUP_INTERVAL);
    cleanup.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

    tracing::info!(batch_size = BATCH_SIZE, "outbox publisher started");
    loop {
        tokio::select! {
            result = publisher.publish_once() => {
                match result {
                    Ok(stats) if stats.claimed == 0 => {
                        tokio::time::sleep(IDLE_DELAY).await;
                    }
                    Ok(stats) => {
                        consecutive_failures =
                            failure_count_after_success(consecutive_failures, stats.published);
                        tracing::info!(
                            claimed = stats.claimed,
                            published = stats.published,
                            publish_failed = stats.publish_failed,
                            mark_failed = stats.mark_failed,
                            claim_lost = stats.claim_lost,
                            "published outbox batch"
                        );
                        if full_batch_delay(stats.claimed, BATCH_SIZE, FULL_BATCH_DELAY).is_some() {
                            tokio::time::sleep(FULL_BATCH_DELAY).await;
                        }
                    }
                    Err(err) => {
                        let delay = failure_backoff(consecutive_failures, MAX_FAILURE_DELAY);
                        consecutive_failures = consecutive_failures.saturating_add(1);
                        tracing::warn!(
                            error = %err,
                            retry_in_seconds = delay.as_secs(),
                            "outbox publish attempt failed"
                        );
                        tokio::time::sleep(delay).await;
                    }
                }
            }
            _ = cleanup.tick() => {
                match publisher.prune_published(PUBLISHED_RETENTION).await {
                    Ok(0) => {}
                    Ok(count) => tracing::info!(count, "pruned published outbox events"),
                    Err(err) => tracing::warn!(error = %err, "failed to prune published outbox events"),
                }
            }
            _ = tokio::signal::ctrl_c() => {
                tracing::info!("outbox publisher stopping");
                return Ok(());
            }
        }
    }
}

fn failure_backoff(consecutive_failures: u32, maximum: Duration) -> Duration {
    let multiplier = 2u32.saturating_pow(consecutive_failures.min(31));
    Duration::from_secs(1)
        .saturating_mul(multiplier)
        .min(maximum)
}

fn failure_count_after_success(previous_failures: u32, published_count: usize) -> u32 {
    if published_count > 0 {
        0
    } else {
        previous_failures
    }
}

fn full_batch_delay(published_count: usize, batch_size: i64, delay: Duration) -> Option<Duration> {
    if i64::try_from(published_count).ok()? >= batch_size {
        Some(delay)
    } else {
        None
    }
}

async fn run_migrations() -> Result<(), Box<dyn std::error::Error>> {
    let database_url = std::env::var("DATABASE_URL")?;
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await?;

    MIGRATOR.run(&pool).await?;

    tracing::info!("database migrations applied");
    Ok(())
}

async fn serve() -> Result<(), Box<dyn std::error::Error>> {
    serve::run().await
}

fn print_usage() {
    eprintln!(
        "usage: engine-rust <migrate|serve|publish-outbox|resolve-alert <alert_id> <resolved_by> <resolution_note>>"
    );
}

fn init_logging() {
    let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
    let json = match std::env::var("LOG_FORMAT").as_deref() {
        Ok("json") => true,
        Ok("pretty") => false,
        _ => !cfg!(debug_assertions),
    };

    if json {
        tracing_subscriber::fmt()
            .json()
            .with_env_filter(filter)
            .init();
    } else {
        tracing_subscriber::fmt()
            .with_target(false)
            .with_env_filter(filter)
            .init();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn outbox_failure_backoff_is_exponential_and_capped() {
        let maximum = Duration::from_secs(30);

        assert_eq!(failure_backoff(0, maximum), Duration::from_secs(1));
        assert_eq!(failure_backoff(1, maximum), Duration::from_secs(2));
        assert_eq!(failure_backoff(4, maximum), Duration::from_secs(16));
        assert_eq!(failure_backoff(5, maximum), maximum);
        assert_eq!(failure_backoff(u32::MAX, maximum), maximum);
    }

    #[test]
    fn idle_outbox_attempt_does_not_reset_failure_count() {
        assert_eq!(failure_count_after_success(2, 0), 2);
        assert_eq!(failure_count_after_success(2, 1), 0);
    }

    #[test]
    fn full_outbox_batches_get_a_small_fairness_delay() {
        let delay = Duration::from_millis(10);

        assert_eq!(full_batch_delay(49, 50, delay), None);
        assert_eq!(full_batch_delay(50, 50, delay), Some(delay));
    }
}
