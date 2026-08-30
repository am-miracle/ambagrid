mod actions;
mod adapters;
mod config;
mod domain;
mod ports;
mod serve;
mod telemetry;

use actions::resolve_alert::{ResolveAlert, ResolveAlertInput};
use adapters::{
    kafka::producer::{KafkaAlertEventPublisher, ProducerConfig},
    postgres::PostgresIngestRepository,
};
use config::Config;
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
    let resolved_by = args[1].clone();
    let resolution_note = args[2..].join(" ");

    let cfg = Config::from_env()?;
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .connect(&cfg.database_url)
        .await?;
    let store = PostgresIngestRepository::new(pool);
    let events = KafkaAlertEventPublisher::connect(ProducerConfig {
        brokers: cfg.kafka_brokers,
        alert_opened_topic: cfg.alert_opened_topic,
        alert_resolved_topic: cfg.alert_resolved_topic,
    })?;
    let action = ResolveAlert::new(&store, &events);

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
        "usage: engine-rust <migrate|serve|resolve-alert <alert_id> <resolved_by> <resolution_note>>"
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
