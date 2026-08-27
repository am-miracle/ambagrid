use sqlx::postgres::PgPoolOptions;

static MIGRATOR: sqlx::migrate::Migrator = sqlx::migrate!();

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
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

    println!("database migrations applied");
    Ok(())
}

async fn serve() -> Result<(), Box<dyn std::error::Error>> {
    println!("engine starting; migrations are not run in serve mode");
    Ok(())
}

fn print_usage() {
    eprintln!("usage: engine-rust <migrate|serve>");
}
