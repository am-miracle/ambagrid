use sqlx::{PgPool, query};

use crate::ports::{DeadLetter, DeadLetterSink, PortError};

#[derive(Debug, Clone)]
pub struct PostgresDeadLetterSink {
    pool: PgPool,
}

impl PostgresDeadLetterSink {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }
}

impl DeadLetterSink for PostgresDeadLetterSink {
    async fn park(&self, entry: &DeadLetter) -> Result<(), PortError> {
        query(
            r#"
            INSERT INTO telemetry_dead_letters (
                kafka_topic, kafka_partition, kafka_offset, payload, stage, error
            )
            VALUES ($1, $2, $3, $4, $5, $6)
            ON CONFLICT (kafka_topic, kafka_partition, kafka_offset) DO NOTHING
            "#,
        )
        .bind(&entry.kafka_topic)
        .bind(entry.kafka_partition)
        .bind(entry.kafka_offset)
        .bind(&entry.payload)
        .bind(entry.stage.as_str())
        .bind(&entry.error)
        .execute(&self.pool)
        .await
        .map_err(PortError::storage)?;

        Ok(())
    }
}
