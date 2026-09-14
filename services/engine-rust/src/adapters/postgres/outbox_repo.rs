use std::{future::Future, time::Duration};

use sqlx::{PgPool, Row, query};
use tokio::time::timeout;
use uuid::Uuid;

use crate::ports::{MarkPublishedOutcome, OutboxEvent, OutboxRepository, PortError};

const DB_OPERATION_TIMEOUT: Duration = Duration::from_secs(5);
const PRUNE_BATCH_SIZE: i64 = 1_000;

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
        let rows = with_db_timeout(
            query(
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
            .fetch_all(&self.pool),
        )
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
        let result = with_db_timeout(
            query(MARK_PUBLISHED_SQL)
                .bind(event_id)
                .bind(claim_id)
                .execute(&self.pool),
        )
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
        let result = with_db_timeout(
            query(
                r#"
            DELETE FROM command_event_outbox
            WHERE event_id IN (
                SELECT event_id
                FROM command_event_outbox
                WHERE published_at < now() - ($1 * interval '1 second')
                ORDER BY published_at, event_id
                LIMIT $2
            )
            "#,
            )
            .bind(retention_seconds)
            .bind(PRUNE_BATCH_SIZE)
            .execute(&self.pool),
        )
        .await
        .map_err(PortError::storage)?;

        Ok(result.rows_affected())
    }
}

async fn with_db_timeout<T>(operation: impl Future<Output = sqlx::Result<T>>) -> sqlx::Result<T> {
    timeout(DB_OPERATION_TIMEOUT, operation)
        .await
        .map_err(|_| sqlx::Error::PoolTimedOut)?
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use sqlx::{PgPool, postgres::PgPoolOptions, query};
    use uuid::Uuid;

    use super::{MARK_PUBLISHED_SQL, PRUNE_BATCH_SIZE, PostgresOutboxRepository};
    use crate::ports::{MarkPublishedOutcome, OutboxRepository};

    #[test]
    fn mark_published_sql_rejects_expired_claims() {
        assert!(
            MARK_PUBLISHED_SQL.contains("claimed_at >= now() - interval '30 seconds'"),
            "mark_published must not mark rows after its lease expires"
        );
    }

    #[tokio::test]
    #[ignore = "requires DATABASE_URL and a writable Postgres database"]
    async fn postgres_claims_do_not_overlap_and_expired_claims_are_fenced() {
        let pool = integration_pool().await;
        sqlx::migrate!().run(&pool).await.unwrap();
        let repo = PostgresOutboxRepository::new(pool.clone());
        let first = Uuid::new_v4();
        let second = Uuid::new_v4();
        insert_unpublished_event(&pool, first, "it-claim-1", 1).await;
        insert_unpublished_event(&pool, second, "it-claim-2", 2).await;

        let claimed_first = repo.claim_unpublished(1).await.unwrap();
        let claimed_second = repo.claim_unpublished(2).await.unwrap();

        assert_eq!(claimed_first.len(), 1);
        assert_eq!(claimed_first[0].event_id, first);
        assert_eq!(claimed_second.len(), 1);
        assert_eq!(claimed_second[0].event_id, second);

        query(
            r#"
            UPDATE command_event_outbox
            SET claimed_at = now() - interval '31 seconds'
            WHERE event_id = $1
            "#,
        )
        .bind(first)
        .execute(&pool)
        .await
        .unwrap();

        let stale_mark = repo
            .mark_published(first, claimed_first[0].claim_id)
            .await
            .unwrap();
        assert_eq!(stale_mark, MarkPublishedOutcome::ClaimLost);

        let reclaimed = repo.claim_unpublished(1).await.unwrap();
        assert_eq!(reclaimed.len(), 1);
        assert_eq!(reclaimed[0].event_id, first);
        assert_ne!(reclaimed[0].claim_id, claimed_first[0].claim_id);

        cleanup_events(&pool, &[first, second]).await;
    }

    #[tokio::test]
    #[ignore = "requires DATABASE_URL and a writable Postgres database"]
    async fn postgres_prunes_published_rows_in_batches() {
        let pool = integration_pool().await;
        sqlx::migrate!().run(&pool).await.unwrap();
        let repo = PostgresOutboxRepository::new(pool.clone());
        let event_ids: Vec<Uuid> = (0..(PRUNE_BATCH_SIZE + 2))
            .map(|_| Uuid::new_v4())
            .collect();
        for (index, event_id) in event_ids.iter().enumerate() {
            insert_published_event(&pool, *event_id, &format!("it-prune-{index}")).await;
        }

        let pruned = repo.prune_published(Duration::from_secs(1)).await.unwrap();
        assert_eq!(pruned, PRUNE_BATCH_SIZE as u64);

        cleanup_events(&pool, &event_ids).await;
    }

    async fn integration_pool() -> PgPool {
        let database_url =
            std::env::var("DATABASE_URL").expect("DATABASE_URL must be set for ignored test");
        PgPoolOptions::new()
            .max_connections(5)
            .connect(&database_url)
            .await
            .unwrap()
    }

    async fn insert_unpublished_event(
        pool: &PgPool,
        event_id: Uuid,
        aggregate_id: &str,
        order: i64,
    ) {
        query(
            r#"
            INSERT INTO command_event_outbox (
                event_id, topic, event_type, aggregate_type, aggregate_id, payload, recorded_at
            )
            VALUES ($1, 'alert.resolved', 'alert.resolved', 'alert', $2, '{}'::jsonb, now() + ($3 * interval '1 second'))
            "#,
        )
        .bind(event_id)
        .bind(aggregate_id)
        .bind(order)
        .execute(pool)
        .await
        .unwrap();
    }

    async fn insert_published_event(pool: &PgPool, event_id: Uuid, aggregate_id: &str) {
        query(
            r#"
            INSERT INTO command_event_outbox (
                event_id, topic, event_type, aggregate_type, aggregate_id, payload, recorded_at, published_at
            )
            VALUES ($1, 'alert.resolved', 'alert.resolved', 'alert', $2, '{}'::jsonb, now() - interval '2 days', now() - interval '2 days')
            "#,
        )
        .bind(event_id)
        .bind(aggregate_id)
        .execute(pool)
        .await
        .unwrap();
    }

    async fn cleanup_events(pool: &PgPool, event_ids: &[Uuid]) {
        query("DELETE FROM command_event_outbox WHERE event_id = ANY($1)")
            .bind(event_ids)
            .execute(pool)
            .await
            .unwrap();
    }
}
