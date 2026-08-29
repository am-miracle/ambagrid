#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Config {
    pub database_url: String,
    pub kafka_brokers: Vec<String>,
    pub telemetry_topic: String,
}

impl Config {
    pub fn from_env() -> Result<Self, ConfigError> {
        let database_url = env_string("DATABASE_URL", None)?;
        let kafka_brokers = env_csv("KAFKA_BROKERS", "localhost:9092");
        let telemetry_topic = env_string("TELEMETRY_TOPIC", Some("telemetry.ingested"))?;

        if kafka_brokers.is_empty() {
            return Err(ConfigError::Invalid(
                "KAFKA_BROKERS must not be empty".to_string(),
            ));
        }

        Ok(Self {
            database_url,
            kafka_brokers,
            telemetry_topic,
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum ConfigError {
    #[error("{0} must be set")]
    Missing(&'static str),
    #[error("{0}")]
    Invalid(String),
}

fn env_string(key: &'static str, fallback: Option<&str>) -> Result<String, ConfigError> {
    let value = std::env::var(key).unwrap_or_default().trim().to_string();
    if value.is_empty() {
        fallback
            .map(str::to_string)
            .ok_or(ConfigError::Missing(key))
    } else {
        Ok(value)
    }
}

fn env_csv(key: &'static str, fallback: &str) -> Vec<String> {
    let raw = std::env::var(key).unwrap_or_else(|_| fallback.to_string());
    raw.split(',')
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(str::to_string)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn env_csv_trims_empty_values() {
        unsafe {
            std::env::set_var("KAFKA_BROKERS", " redpanda:9092, ,localhost:9092 ");
        }

        assert_eq!(
            env_csv("KAFKA_BROKERS", "fallback:9092"),
            vec!["redpanda:9092", "localhost:9092"]
        );
    }
}
