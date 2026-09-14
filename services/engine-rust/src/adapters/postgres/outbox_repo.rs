use std::time::Duration;

use sqlx::{PgPool, Row, query};
use uuid::Uuid;

use crate::ports::{MarkPublishedOutcome, OutboxEvent, OutboxRepository, PortError};

const MARK_PUBLISHED_SQL: &str = r#"
UPDATE command_event_outbox
SET published_at = now()
WHERE event_id = $1
  AND claim_id = $2
  AND claimed_at >= now() - interval '30 seconds'
  AND published_at IS NULL
"#;

#[derive(Debug, Clone)]
pub struct PostgresOutboxRepository {
    pool: PgPool,
}

impl PostgresOutboxRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }
}

impl OutboxRepository for PostgresOutboxRepository {
    async fn claim_unpublished(&self, limit: i64) -> Result<Vec<OutboxEvent>, PortError> {
        let claim_id = Uuid::new_v4();
        let rows = query(
            r#"
            WITH claim AS (
                SELECT event_id
                FROM command_event_outbox
                WHERE published_at IS NULL
                  AND (claimed_at IS NULL OR claimed_at < now() - interval '30 seconds')
                ORDER BY recorded_at, event_id
                LIMIT $1
                FOR UPDATE SKIP LOCKED
            )
            UPDATE command_event_outbox AS outbox
            SET claim_id = $2,
                claimed_at = now()
            FROM claim
            WHERE outbox.event_id = claim.event_id
            RETURNING outbox.event_id, outbox.claim_id, outbox.topic, outbox.event_type,
                outbox.aggregate_id,
                outbox.payload::text AS payload
            "#,
        )
        .bind(limit)
        .bind(claim_id)
        .fetch_all(&self.pool)
        .await
        .map_err(PortError::storage)?;

        Ok(rows
            .into_iter()
            .map(|row| OutboxEvent {
                event_id: row.get::<Uuid, _>("event_id"),
                claim_id: row.get::<Uuid, _>("claim_id"),
                topic: row.get("topic"),
                event_type: row.get("event_type"),
                aggregate_id: row.get("aggregate_id"),
                payload: row.get::<String, _>("payload").into_bytes(),
            })
            .collect())
    }

    async fn mark_published(
        &self,
        event_id: Uuid,
        claim_id: Uuid,
    ) -> Result<MarkPublishedOutcome, PortError> {
        let result = query(MARK_PUBLISHED_SQL)
            .bind(event_id)
            .bind(claim_id)
            .execute(&self.pool)
            .await
            .map_err(PortError::storage)?;

        if result.rows_affected() != 1 {
            return Ok(MarkPublishedOutcome::ClaimLost);
        }

        Ok(MarkPublishedOutcome::Marked)
    }

    async fn prune_published(&self, retention: Duration) -> Result<u64, PortError> {
        let retention_seconds = i64::try_from(retention.as_secs())
            .map_err(|_| PortError::message("outbox retention is too large"))?;
        let result = query(
            r#"
            DELETE FROM command_event_outbox
            WHERE published_at < now() - ($1 * interval '1 second')
            "#,
        )
        .bind(retention_seconds)
        .execute(&self.pool)
        .await
        .map_err(PortError::storage)?;

        Ok(result.rows_affected())
    }
}

#[cfg(test)]
mod tests {
    use super::MARK_PUBLISHED_SQL;

    #[test]
    fn mark_published_sql_rejects_expired_claims() {
        assert!(
            MARK_PUBLISHED_SQL.contains("claimed_at >= now() - interval '30 seconds'"),
            "mark_published must not mark rows after its lease expires"
        );
    }
}
