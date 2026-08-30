use std::{
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
    time::Duration,
};

use futures_util::StreamExt;
use rskafka::client::{
    ClientBuilder,
    consumer::{StartOffset, StreamConsumerBuilder},
    partition::UnknownTopicHandling,
};

use crate::{
    actions::ingest_reading::IngestReading,
    domain::asset::Reading,
    ports::{
        AlertEvents, AlertRepository, AssetRepository, DeadLetter, DeadLetterSink, DeadLetterStage,
        PortError, ReadingsSink,
    },
    telemetry::decode::decode_metric_payload,
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ConsumerConfig {
    pub brokers: Vec<String>,
    pub topic: String,
}

// Bounded and short: this is a live telemetry stream, not a batch job. The
// goal is smoothing over a brief connection blip, not waiting out a real
// outage — if Postgres is actually down, no retry budget short of "forever"
// fixes that, and "forever" would stall every message behind it.
const MAX_INGEST_ATTEMPTS: u32 = 3;

fn ingest_backoff(attempt: u32) -> Duration {
    Duration::from_millis(100 * 4u64.pow(attempt - 1))
}

#[derive(Debug, Default)]
struct ConsumerStats {
    decode_failed: AtomicU64,
    ingest_failed: AtomicU64,
    dlq_failed: AtomicU64,
}

impl ConsumerStats {
    fn add_decode_failed(&self) -> u64 {
        self.decode_failed.fetch_add(1, Ordering::Relaxed) + 1
    }

    fn add_ingest_failed(&self) -> u64 {
        self.ingest_failed.fetch_add(1, Ordering::Relaxed) + 1
    }

    fn add_dlq_failed(&self) -> u64 {
        self.dlq_failed.fetch_add(1, Ordering::Relaxed) + 1
    }
}

// Single-partition assumption: rskafka has no consumer-group/rebalance
// protocol, so this reads partition 0 only. Fine for one engine replica;
// running more than one against a multi-partition topic needs a real
// partition-assignment scheme first.
pub async fn run<A, R, L, E, D>(
    config: ConsumerConfig,
    ingest: &IngestReading<'_, A, R, L, E>,
    dead_letters: &D,
) -> Result<(), ConsumerError>
where
    A: AssetRepository,
    R: ReadingsSink,
    L: AlertRepository,
    E: AlertEvents,
    D: DeadLetterSink,
{
    let topic = config.topic.clone();
    let client = ClientBuilder::new(config.brokers)
        .build()
        .await
        .map_err(ConsumerError::Connect)?;

    let partition_client = Arc::new(
        client
            .partition_client(config.topic, 0, UnknownTopicHandling::Retry)
            .await
            .map_err(ConsumerError::Connect)?,
    );

    // Latest: process new readings only. Restarting the engine does not
    // replay history, since there is nowhere yet to persist a resume offset.
    let mut stream = StreamConsumerBuilder::new(partition_client, StartOffset::Latest).build();
    let stats = ConsumerStats::default();

    while let Some(next) = stream.next().await {
        let (record_and_offset, _high_watermark) = next.map_err(ConsumerError::Consume)?;

        let Some(bytes) = record_and_offset.record.value else {
            continue;
        };

        let reading = match decode_metric_payload(&bytes) {
            Ok(reading) => reading,
            Err(err) => {
                tracing::warn!(
                    error = %err,
                    decode_failed = stats.add_decode_failed(),
                    "parking malformed telemetry message"
                );

                let entry = build_dead_letter(
                    &topic,
                    record_and_offset.offset,
                    bytes,
                    DeadLetterStage::Decode,
                    &err,
                );
                park(&stats, dead_letters, &entry).await;

                continue;
            }
        };

        if let Err(err) = ingest_with_retry(ingest, &reading).await {
            tracing::error!(
                error = %err,
                ingest_failed = stats.add_ingest_failed(),
                "failed to ingest telemetry reading after retries"
            );

            let entry = build_dead_letter(
                &topic,
                record_and_offset.offset,
                bytes,
                DeadLetterStage::Ingest,
                &err,
            );
            park(&stats, dead_letters, &entry).await;
        }
    }

    Ok(())
}

// Retries only PortError::Storage (assumed transient); a data-shape error
// (PortError::Message) won't be fixed by retrying, so it's returned
// immediately.
async fn ingest_with_retry<A, R, L, E>(
    ingest: &IngestReading<'_, A, R, L, E>,
    reading: &Reading,
) -> Result<(), PortError>
where
    A: AssetRepository,
    R: ReadingsSink,
    L: AlertRepository,
    E: AlertEvents,
{
    for attempt in 1..=MAX_INGEST_ATTEMPTS {
        match ingest.execute(reading.clone()).await {
            Ok(_) => return Ok(()),
            Err(err) if err.is_retryable() && attempt < MAX_INGEST_ATTEMPTS => {
                tracing::warn!(
                    error = %err,
                    attempt,
                    "retrying telemetry ingest after transient failure"
                );
                tokio::time::sleep(ingest_backoff(attempt)).await;
            }
            Err(err) => return Err(err),
        }
    }
    unreachable!("loop always returns by the last attempt")
}

async fn park(stats: &ConsumerStats, dead_letters: &impl DeadLetterSink, entry: &DeadLetter) {
    if let Err(err) = dead_letters.park(entry).await {
        tracing::error!(
            error = %err,
            dlq_failed = stats.add_dlq_failed(),
            "failed to park telemetry message"
        );
    }
}

// Partition is always 0: see the single-partition assumption on `run`.
fn build_dead_letter(
    topic: &str,
    offset: i64,
    payload: Vec<u8>,
    stage: DeadLetterStage,
    error: impl std::fmt::Display,
) -> DeadLetter {
    DeadLetter {
        kafka_topic: topic.to_string(),
        kafka_partition: 0,
        kafka_offset: offset,
        payload,
        stage,
        error: error.to_string(),
    }
}

#[derive(Debug, thiserror::Error)]
pub enum ConsumerError {
    #[error("failed to connect to Kafka: {0}")]
    Connect(#[source] rskafka::client::error::Error),
    #[error("failed to consume from Kafka: {0}")]
    Consume(#[source] rskafka::client::error::Error),
}

#[cfg(test)]
mod tests {
    use crate::telemetry::decode::DecodeError;

    use super::*;

    #[test]
    fn build_dead_letter_preserves_original_payload_and_offset() {
        let payload = b"not a valid metric payload".to_vec();
        let error = DecodeError::MissingDeviceId;

        let entry = build_dead_letter(
            "telemetry.ingested",
            42,
            payload.clone(),
            DeadLetterStage::Decode,
            &error,
        );

        assert_eq!(entry.kafka_topic, "telemetry.ingested");
        assert_eq!(entry.kafka_partition, 0);
        assert_eq!(entry.kafka_offset, 42);
        assert_eq!(entry.payload, payload);
        assert_eq!(entry.stage, DeadLetterStage::Decode);
        assert_eq!(entry.error, error.to_string());
    }

    #[test]
    fn build_dead_letter_marks_ingest_stage_for_ingest_failures() {
        let payload = b"a validly-decoded but unpersisted reading".to_vec();
        let error = PortError::message("unknown alert severity: bogus");

        let entry = build_dead_letter(
            "telemetry.ingested",
            7,
            payload.clone(),
            DeadLetterStage::Ingest,
            &error,
        );

        assert_eq!(entry.stage, DeadLetterStage::Ingest);
        assert_eq!(entry.payload, payload);
        assert_eq!(entry.error, error.to_string());
    }

    #[test]
    fn ingest_backoff_grows_exponentially() {
        assert_eq!(ingest_backoff(1), Duration::from_millis(100));
        assert_eq!(ingest_backoff(2), Duration::from_millis(400));
        assert_eq!(ingest_backoff(3), Duration::from_millis(1600));
    }
}
