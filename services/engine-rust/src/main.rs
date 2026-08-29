mod actions;
mod adapters;
mod config;
mod domain;
mod ports;
mod serve;
mod telemetry;

use sqlx::postgres::PgPoolOptions;
use tracing_subscriber::EnvFilter;

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
    eprintln!("usage: engine-rust <migrate|serve>");
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
