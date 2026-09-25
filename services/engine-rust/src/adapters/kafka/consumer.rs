use std::{
    sync::atomic::{AtomicU64, Ordering},
    time::Duration,
};

use rdkafka::{
    Message,
    consumer::{Consumer, StreamConsumer},
    error::KafkaError,
};

use super::client_config;
use crate::{
    actions::ingest_reading::IngestReading,
    config::KafkaSecurity,
    domain::asset::Reading,
    ports::{DeadLetter, DeadLetterSink, DeadLetterStage, IngestRepository, PortError},
    telemetry::decode::decode_metric_payload,
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ConsumerConfig {
    pub brokers: Vec<String>,
    pub security: KafkaSecurity,
    pub topic: String,
    pub group_id: String,
}

const MAX_INGEST_ATTEMPTS: u32 = 3;

// Paced so a broker that is down cannot spin this loop on back-to-back errors.
const CONSUME_ERROR_BACKOFF: Duration = Duration::from_millis(500);

fn ingest_backoff(attempt: u32) -> Duration {
    Duration::from_millis(100u64.saturating_mul(4u64.saturating_pow(attempt.saturating_sub(1))))
}

#[derive(Debug, Default)]
struct ConsumerStats {
    consume_failed: AtomicU64,
    decode_failed: AtomicU64,
    ingest_failed: AtomicU64,
    dlq_failed: AtomicU64,
    store_offset_failed: AtomicU64,
}

impl ConsumerStats {
    fn add_consume_failed(&self) -> u64 {
        self.consume_failed.fetch_add(1, Ordering::Relaxed) + 1
    }

    fn add_decode_failed(&self) -> u64 {
        self.decode_failed.fetch_add(1, Ordering::Relaxed) + 1
    }

    fn add_ingest_failed(&self) -> u64 {
        self.ingest_failed.fetch_add(1, Ordering::Relaxed) + 1
    }

    fn add_dlq_failed(&self) -> u64 {
        self.dlq_failed.fetch_add(1, Ordering::Relaxed) + 1
    }

    fn add_store_offset_failed(&self) -> u64 {
        self.store_offset_failed.fetch_add(1, Ordering::Relaxed) + 1
    }
}

pub async fn run<S, D>(
    config: ConsumerConfig,
    ingest: &IngestReading<'_, S>,
    dead_letters: &D,
) -> Result<(), ConsumerError>
where
    S: IngestRepository,
    D: DeadLetterSink,
{
    let consumer: StreamConsumer = client_config(&config.brokers, &config.security)
        .set("group.id", &config.group_id)
        .set("auto.offset.reset", "earliest")
        .set("enable.auto.commit", "true")
        .set("auto.commit.interval.ms", "5000")
        .set("enable.auto.offset.store", "false")
        .create()
        .map_err(ConsumerError::Connect)?;

    consumer
        .subscribe(&[config.topic.as_str()])
        .map_err(ConsumerError::Connect)?;

    let stats = ConsumerStats::default();

    loop {
        // rdkafka reconnects and rebalances internally, so a recv() error is
        // almost always a transient broker or protocol blip. Returning here
        // would end ingestion for every site until the process is restarted,
        // so log it and keep consuming instead.
        let message = match consumer.recv().await {
            Ok(message) => message,
            Err(err) => {
                let consume_failed = stats.add_consume_failed();
                tracing::error!(
                    error = %err,
                    consume_failed,
                    "kafka receive failed; retrying"
                );
                tokio::time::sleep(CONSUME_ERROR_BACKOFF).await;
                continue;
            }
        };

        let topic = message.topic().to_string();
        let partition = message.partition();
        let offset = message.offset();
        let handled = match message.payload().map(<[u8]>::to_vec) {
            Some(bytes) => {
                handle_payload(
                    ingest,
                    dead_letters,
                    &stats,
                    &topic,
                    partition,
                    offset,
                    bytes,
                )
                .await
            }
            None => true,
        };

        if !handled {
            continue;
        }

        // Stored once the record is fully handled either way (ingested or
        // dead-lettered), so a message that's permanently unprocessable
        // doesn't get redelivered forever, and a crash before this point
        // simply redelivers the record on the next rebalance/restart.
        if let Err(err) = consumer.store_offset(&topic, partition, offset) {
            let store_offset_failed = stats.add_store_offset_failed();
            tracing::error!(
                error = %err,
                partition,
                offset,
                store_offset_failed,
                "failed to store Kafka offset for commit"
            );
        }
    }
}

async fn handle_payload<S, D>(
    ingest: &IngestReading<'_, S>,
    dead_letters: &D,
    stats: &ConsumerStats,
    topic: &str,
    partition: i32,
    offset: i64,
    bytes: Vec<u8>,
) -> bool
where
    S: IngestRepository,
    D: DeadLetterSink,
{
    match decode_metric_payload(&bytes) {
        Ok(mut reading) => {
            reading.source_event_id = Some(source_event_id(topic, partition, offset));
            if let Err(err) = ingest_with_retry(ingest, &reading).await {
                let ingest_failed = stats.add_ingest_failed();
                tracing::error!(
                    error = %err,
                    ingest_failed,
                    "failed to ingest telemetry reading after retries; leaving offset unstored for redelivery"
                );
                return false;
            }

            true
        }
        Err(err) => {
            let decode_failed = stats.add_decode_failed();
            tracing::warn!(
                error = %err,
                decode_failed,
                "parking malformed telemetry message"
            );

            let entry = build_dead_letter(
                topic,
                partition,
                offset,
                bytes,
                DeadLetterStage::Decode,
                &err,
            );
            park(stats, dead_letters, &entry).await
        }
    }
}

fn source_event_id(topic: &str, partition: i32, offset: i64) -> String {
    format!("{topic}:{partition}:{offset}")
}

// Retries only PortError::Storage (assumed transient); a data-shape error
// (PortError::Message) won't be fixed by retrying, so it's returned
// immediately.
async fn ingest_with_retry<S>(
    ingest: &IngestReading<'_, S>,
    reading: &Reading,
) -> Result<(), PortError>
where
    S: IngestRepository,
{
    let mut attempt = 1;
    loop {
        match ingest.execute(reading.clone()).await {
            Ok(_) => return Ok(()),
            Err(err) if err.is_retryable() && attempt < MAX_INGEST_ATTEMPTS => {
                tracing::warn!(
                    error = %err,
                    attempt,
                    "retrying telemetry ingest after transient failure"
                );
                tokio::time::sleep(ingest_backoff(attempt)).await;
                attempt += 1;
            }
            Err(err) => return Err(err),
        }
    }
}

async fn park(
    stats: &ConsumerStats,
    dead_letters: &impl DeadLetterSink,
    entry: &DeadLetter,
) -> bool {
    if let Err(err) = dead_letters.park(entry).await {
        let dlq_failed = stats.add_dlq_failed();
        tracing::error!(
            error = %err,
            dlq_failed,
            "failed to park telemetry message"
        );
        return false;
    }

    true
}

fn build_dead_letter(
    topic: &str,
    partition: i32,
    offset: i64,
    payload: Vec<u8>,
    stage: DeadLetterStage,
    error: impl std::fmt::Display,
) -> DeadLetter {
    DeadLetter {
        kafka_topic: topic.to_string(),
        kafka_partition: partition,
        kafka_offset: offset,
        payload,
        stage,
        error: error.to_string(),
    }
}

#[derive(Debug, thiserror::Error)]
pub enum ConsumerError {
    #[error("failed to connect to Kafka: {0}")]
    Connect(#[source] KafkaError),
}

#[cfg(test)]
mod tests {
    use std::sync::{Mutex, atomic::AtomicU32};

    use chrono::Utc;
    use prost::Message as _;

    use crate::{
        domain::asset::{Asset, AssetState, SmartMeterState},
        ports::{IngestWrite, PolicyOutcome},
        telemetry::{
            decode::DecodeError,
            proto::{DeviceType, ElectricalMetrics, MetricPayload},
        },
    };

    use super::*;

    fn sample_reading() -> Reading {
        Reading {
            asset: Asset {
                asset_id: "met-0101".to_string(),
                site_id: "ng-kaji-01".to_string(),
                internal_temperature: Some(38.0),
                last_seen_at: Utc::now(),
            },
            observed_at: Utc::now(),
            source_event_id: None,
            state: AssetState::SmartMeter(SmartMeterState::default()),
        }
    }

    fn valid_payload_bytes() -> Vec<u8> {
        let payload = MetricPayload {
            device_id: "met-0101".to_string(),
            device_type: DeviceType::SmartMeter as i32,
            timestamp_utc: Some(1_787_990_400),
            site_id: "ng-kaji-01".to_string(),
            metrics: Some(ElectricalMetrics::default()),
            ..MetricPayload::default()
        };
        let mut bytes = Vec::new();
        payload.encode(&mut bytes).unwrap();
        bytes
    }

    #[test]
    fn source_event_id_uses_kafka_record_coordinates() {
        assert_eq!(
            source_event_id("telemetry.ingested", 3, 918),
            "telemetry.ingested:3:918"
        );
    }

    // Fails with a storage (transient) error on every attempt below
    // `fail_until`, then succeeds. `fail_until = u32::MAX` never succeeds.
    #[derive(Default)]
    struct FlakyStore {
        attempts: AtomicU32,
        fail_until: u32,
    }

    impl IngestRepository for FlakyStore {
        async fn ingest(
            &self,
            _reading: &Reading,
            _outcome: PolicyOutcome,
        ) -> Result<IngestWrite, PortError> {
            let attempt = self.attempts.fetch_add(1, Ordering::Relaxed) + 1;
            if attempt <= self.fail_until {
                Err(PortError::storage(std::io::Error::other(
                    "db connection reset",
                )))
            } else {
                Ok(IngestWrite::default())
            }
        }
    }

    #[derive(Default)]
    struct DataShapeErrorStore {
        attempts: AtomicU32,
    }

    impl IngestRepository for DataShapeErrorStore {
        async fn ingest(
            &self,
            _reading: &Reading,
            _outcome: PolicyOutcome,
        ) -> Result<IngestWrite, PortError> {
            self.attempts.fetch_add(1, Ordering::Relaxed);
            Err(PortError::message("unknown alert severity: bogus"))
        }
    }

    #[derive(Default)]
    struct RecordingDeadLetterSink {
        entries: Mutex<Vec<DeadLetter>>,
        fail: bool,
    }

    impl DeadLetterSink for RecordingDeadLetterSink {
        async fn park(&self, entry: &DeadLetter) -> Result<(), PortError> {
            if self.fail {
                return Err(PortError::message("dlq unavailable"));
            }

            self.entries.lock().unwrap().push(DeadLetter {
                kafka_topic: entry.kafka_topic.clone(),
                kafka_partition: entry.kafka_partition,
                kafka_offset: entry.kafka_offset,
                payload: entry.payload.clone(),
                stage: entry.stage,
                error: entry.error.clone(),
            });
            Ok(())
        }
    }

    #[tokio::test]
    async fn ingest_with_retry_succeeds_after_a_transient_failure() {
        let store = FlakyStore {
            attempts: AtomicU32::new(0),
            fail_until: 1,
        };
        let ingest = IngestReading::new(&store);

        let result = ingest_with_retry(&ingest, &sample_reading()).await;

        assert!(result.is_ok());
        assert_eq!(store.attempts.load(Ordering::Relaxed), 2);
    }

    #[tokio::test]
    async fn ingest_with_retry_gives_up_after_max_attempts_on_persistent_storage_errors() {
        let store = FlakyStore {
            attempts: AtomicU32::new(0),
            fail_until: u32::MAX,
        };
        let ingest = IngestReading::new(&store);

        let result = ingest_with_retry(&ingest, &sample_reading()).await;

        assert!(result.is_err());
        assert_eq!(store.attempts.load(Ordering::Relaxed), MAX_INGEST_ATTEMPTS);
    }

    #[tokio::test]
    async fn ingest_with_retry_does_not_retry_data_shape_errors() {
        let store = DataShapeErrorStore::default();
        let ingest = IngestReading::new(&store);

        let result = ingest_with_retry(&ingest, &sample_reading()).await;

        assert!(result.is_err());
        assert_eq!(store.attempts.load(Ordering::Relaxed), 1);
    }

    #[test]
    fn build_dead_letter_preserves_original_payload_partition_and_offset() {
        let payload = b"not a valid metric payload".to_vec();
        let error = DecodeError::MissingDeviceId;

        let entry = build_dead_letter(
            "telemetry.ingested",
            2,
            42,
            payload.clone(),
            DeadLetterStage::Decode,
            &error,
        );

        assert_eq!(entry.kafka_topic, "telemetry.ingested");
        assert_eq!(entry.kafka_partition, 2);
        assert_eq!(entry.kafka_offset, 42);
        assert_eq!(entry.payload, payload);
        assert_eq!(entry.stage, DeadLetterStage::Decode);
        assert_eq!(entry.error, error.to_string());
    }

    #[tokio::test]
    async fn post_decode_storage_failures_are_left_unhandled_for_redelivery() {
        let store = FlakyStore {
            attempts: AtomicU32::new(0),
            fail_until: u32::MAX,
        };
        let ingest = IngestReading::new(&store);
        let sink = RecordingDeadLetterSink::default();
        let stats = ConsumerStats::default();

        let handled = handle_payload(
            &ingest,
            &sink,
            &stats,
            "telemetry.ingested",
            0,
            7,
            valid_payload_bytes(),
        )
        .await;

        assert!(!handled);
        assert_eq!(store.attempts.load(Ordering::Relaxed), MAX_INGEST_ATTEMPTS);
        assert!(sink.entries.lock().unwrap().is_empty());
        assert_eq!(stats.ingest_failed.load(Ordering::Relaxed), 1);
    }

    #[tokio::test]
    async fn park_reports_handled_when_dead_letter_sink_succeeds() {
        let stats = ConsumerStats::default();
        let sink = RecordingDeadLetterSink::default();
        let entry = build_dead_letter(
            "telemetry.ingested",
            0,
            7,
            b"bad reading".to_vec(),
            DeadLetterStage::Decode,
            DecodeError::MissingMetrics,
        );

        let handled = park(&stats, &sink, &entry).await;

        assert!(handled);
        assert_eq!(sink.entries.lock().unwrap().len(), 1);
        assert_eq!(stats.dlq_failed.load(Ordering::Relaxed), 0);
    }

    #[tokio::test]
    async fn park_reports_unhandled_when_dead_letter_sink_fails() {
        let stats = ConsumerStats::default();
        let sink = RecordingDeadLetterSink {
            entries: Mutex::default(),
            fail: true,
        };
        let entry = build_dead_letter(
            "telemetry.ingested",
            0,
            7,
            b"bad reading".to_vec(),
            DeadLetterStage::Decode,
            DecodeError::MissingMetrics,
        );

        let handled = park(&stats, &sink, &entry).await;

        assert!(!handled);
        assert!(sink.entries.lock().unwrap().is_empty());
        assert_eq!(stats.dlq_failed.load(Ordering::Relaxed), 1);
    }

    #[test]
    fn ingest_backoff_grows_exponentially() {
        assert_eq!(ingest_backoff(1), Duration::from_millis(100));
        assert_eq!(ingest_backoff(2), Duration::from_millis(400));
        assert_eq!(ingest_backoff(3), Duration::from_millis(1600));
    }

    // The retry loop must not depend on MAX_INGEST_ATTEMPTS to avoid a panic,
    // and the backoff must stay finite for any attempt number.
    #[test]
    fn ingest_backoff_saturates_instead_of_overflowing() {
        assert_eq!(ingest_backoff(0), Duration::from_millis(100));
        assert_eq!(ingest_backoff(u32::MAX), Duration::from_millis(u64::MAX));
    }
}
