use std::{collections::BTreeMap, future::Future};

use super::{client_config, proto};
use crate::{
    alert_outbox::{AlertOpenedPayload, AlertResolvedPayload, AlertSeverity},
    config::KafkaSecurity,
    meter_command_outbox::MeterCommandRequestedPayload,
    metrics::Metrics,
    ports::{DeadLetter, DeadLetterSink, OutboxEvent, OutboxEventType, OutboxEvents, PortError},
    revenue_outbox::{CreditIssuedPayload, PaymentConfirmedPayload},
};
use prost::Message;
use rdkafka::{
    message::{Header, OwnedHeaders},
    producer::{FutureProducer, FutureRecord},
};

// A single-record producer, kept as a trait so tests can substitute a fake
// and assert on topic routing/payloads without a live broker.
pub(crate) trait RecordSink: Send + Sync {
    fn send(&self, record: KafkaRecord) -> impl Future<Output = Result<(), PortError>> + Send;
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct KafkaRecord {
    topic: String,
    key: Option<Vec<u8>>,
    payload: Vec<u8>,
    headers: BTreeMap<String, Vec<u8>>,
}

impl RecordSink for FutureProducer {
    async fn send(&self, record: KafkaRecord) -> Result<(), PortError> {
        let mut headers = OwnedHeaders::new_with_capacity(record.headers.len());
        for (key, value) in &record.headers {
            headers = headers.insert(Header {
                key,
                value: Some(value.as_slice()),
            });
        }

        let mut future_record = FutureRecord::to(record.topic.as_str()).payload(&record.payload);
        if let Some(key) = &record.key {
            future_record = future_record.key(key);
        }
        if !record.headers.is_empty() {
            future_record = future_record.headers(headers);
        }

        self.send(future_record, std::time::Duration::from_secs(5))
            .await
            .map_err(|(err, _message)| PortError::storage(err))?;

        Ok(())
    }
}

pub struct KafkaDeadLetterPublisher<S: RecordSink = FutureProducer> {
    topic: String,
    sink: S,
    metrics: Metrics,
}

pub struct KafkaOutboxEventPublisher<S: RecordSink = FutureProducer> {
    sink: S,
}

impl KafkaDeadLetterPublisher<FutureProducer> {
    pub fn connect(
        brokers: &[String],
        security: &KafkaSecurity,
        topic: String,
        metrics: Metrics,
    ) -> Result<Self, rdkafka::error::KafkaError> {
        Ok(Self {
            topic,
            sink: future_producer(brokers, security)?,
            metrics,
        })
    }
}

impl KafkaOutboxEventPublisher<FutureProducer> {
    pub fn connect(
        brokers: &[String],
        security: &KafkaSecurity,
    ) -> Result<Self, rdkafka::error::KafkaError> {
        Ok(Self {
            sink: future_producer(brokers, security)?,
        })
    }
}

fn future_producer(
    brokers: &[String],
    security: &KafkaSecurity,
) -> Result<FutureProducer, rdkafka::error::KafkaError> {
    client_config(brokers, security)
        .set("message.timeout.ms", "5000")
        .create()
}

impl<S: RecordSink> DeadLetterSink for KafkaDeadLetterPublisher<S> {
    async fn park(&self, entry: &DeadLetter) -> Result<(), PortError> {
        self.sink
            .send(dead_letter_record(&self.topic, entry))
            .await?;
        self.metrics.record_dlq_parked();
        Ok(())
    }
}

impl<S: RecordSink> OutboxEvents for KafkaOutboxEventPublisher<S> {
    async fn publish(&self, event: &OutboxEvent) -> Result<(), PortError> {
        let payload = encode_outbox_payload(event, &event.event_type)?;
        self.sink
            .send(kafka_record(
                &event.topic,
                Some(event.aggregate_id.as_bytes().to_vec()),
                payload,
                BTreeMap::from([(
                    "event_id".to_string(),
                    event.event_id.to_string().into_bytes(),
                )]),
            ))
            .await
    }
}

impl AlertSeverity {
    fn as_proto(self) -> i32 {
        (match self {
            Self::Info => proto::Severity::Info,
            Self::Warning => proto::Severity::Warning,
            Self::Critical => proto::Severity::Critical,
        }) as i32
    }
}

fn encode_outbox_payload(
    event: &OutboxEvent,
    event_type: &OutboxEventType,
) -> Result<Vec<u8>, PortError> {
    // Topics are deployment-configurable; event_type is the stable discriminator
    // that selects the protobuf contract written to any configured topic.
    match event_type {
        OutboxEventType::AlertOpened => {
            let payload: AlertOpenedPayload = decode(event)?;
            Ok(proto::AlertOpened {
                alert_id: payload.alert_id,
                asset_id: payload.asset_id,
                site_id: payload.site_id,
                severity: payload.severity.as_proto(),
                reason: payload.reason,
                opened_at_utc: payload.opened_at_utc,
                source_event_id: payload.source_event_id,
            }
            .encode_to_vec())
        }
        OutboxEventType::AlertResolved => {
            let payload: AlertResolvedPayload = decode(event)?;
            Ok(proto::AlertResolved {
                alert_id: payload.alert_id,
                asset_id: payload.asset_id,
                site_id: payload.site_id,
                severity: payload.severity.as_proto(),
                reason: payload.reason,
                opened_at_utc: payload.opened_at_utc,
                resolved_at_utc: payload.resolved_at_utc,
                resolution_note: payload.resolution_note,
                resolved_by: payload.resolved_by,
            }
            .encode_to_vec())
        }
        OutboxEventType::PaymentConfirmed => {
            let payload: PaymentConfirmedPayload = decode(event)?;
            let confirmed_at_utc = parse_event_time(event, "confirmed_at", &payload.confirmed_at)?;
            Ok(proto::PaymentConfirmed {
                payment_id: payload.payment_id,
                provider: payload.provider,
                external_reference: payload.external_reference,
                customer_id: payload.customer_id,
                amount_minor_units: payload.amount_minor_units,
                currency: payload.currency,
                confirmed_at_utc,
            }
            .encode_to_vec())
        }
        OutboxEventType::CreditIssued => {
            let payload: CreditIssuedPayload = decode(event)?;
            let created_at_utc = parse_event_time(event, "created_at", &payload.created_at)?;
            Ok(proto::CreditIssued {
                credit_id: payload.credit_id,
                site_id: payload.site_id,
                assignment_id: payload.assignment_id,
                payment_id: payload.payment_id,
                tariff_plan_id: payload.tariff_plan_id,
                source_type: payload.source_type,
                source_id: payload.source_id,
                kwh_granted: payload.kwh_granted,
                money_value_minor_units: payload.money_value_minor_units,
                created_at_utc,
            }
            .encode_to_vec())
        }
        OutboxEventType::MeterCommandRequested => {
            let payload: MeterCommandRequestedPayload = decode(event)?;
            let requested_at_utc = parse_event_time(event, "requested_at", &payload.requested_at)?;
            Ok(proto::MeterCommandRequested {
                command_id: payload.command_id,
                meter_id: payload.meter_id,
                site_id: payload.site_id,
                assignment_id: payload.assignment_id,
                command_type: payload.command_type,
                status: payload.status,
                requested_by: payload.requested_by,
                reason: payload.reason,
                requested_at_utc,
            }
            .encode_to_vec())
        }
        OutboxEventType::Unsupported(value) => Err(PortError::message(format!(
            "unsupported outbox event type {value} for event {}",
            event.event_id
        ))),
    }
}

fn decode<T: serde::de::DeserializeOwned>(event: &OutboxEvent) -> Result<T, PortError> {
    serde_json::from_slice(&event.payload).map_err(|err| invalid_outbox_payload(event, err))
}

fn parse_event_time(event: &OutboxEvent, field: &str, value: &str) -> Result<i64, PortError> {
    chrono::DateTime::parse_from_rfc3339(value)
        .map(|dt| dt.timestamp())
        .map_err(|err| {
            PortError::message(format!(
                "invalid {field} in {} event {}: {err}",
                event.event_type, event.event_id
            ))
        })
}

fn invalid_outbox_payload(event: &OutboxEvent, error: serde_json::Error) -> PortError {
    PortError::message(format!(
        "invalid {} outbox payload for event {}: {error}",
        event.event_type, event.event_id
    ))
}

fn kafka_record(
    topic: &str,
    key: Option<Vec<u8>>,
    payload: Vec<u8>,
    headers: BTreeMap<String, Vec<u8>>,
) -> KafkaRecord {
    KafkaRecord {
        topic: topic.to_string(),
        key,
        payload,
        headers,
    }
}

fn dead_letter_record(topic: &str, entry: &DeadLetter) -> KafkaRecord {
    kafka_record(
        topic,
        Some(
            format!(
                "{}:{}:{}",
                entry.kafka_topic, entry.kafka_partition, entry.kafka_offset
            )
            .into_bytes(),
        ),
        entry.payload.clone(),
        BTreeMap::from([
            (
                "source_topic".to_string(),
                entry.kafka_topic.as_bytes().to_vec(),
            ),
            (
                "source_partition".to_string(),
                entry.kafka_partition.to_string().into_bytes(),
            ),
            (
                "source_offset".to_string(),
                entry.kafka_offset.to_string().into_bytes(),
            ),
            (
                "stage".to_string(),
                entry.stage.as_str().as_bytes().to_vec(),
            ),
            ("error".to_string(), entry.error.as_bytes().to_vec()),
        ]),
    )
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use prost::Message;
    use uuid::Uuid;

    use super::*;
    use crate::ports::DeadLetterStage;

    #[derive(Default)]
    struct FakeSink {
        sent: Mutex<Vec<KafkaRecord>>,
    }

    impl RecordSink for FakeSink {
        async fn send(&self, record: KafkaRecord) -> Result<(), PortError> {
            self.sent.lock().unwrap().push(record);
            Ok(())
        }
    }

    #[tokio::test]
    async fn dead_letter_publisher_preserves_payload_and_source_metadata() {
        let metrics = Metrics::default();
        let publisher = KafkaDeadLetterPublisher {
            topic: "telemetry.ingested.dlq".to_string(),
            sink: FakeSink::default(),
            metrics: metrics.clone(),
        };
        let payload = b"malformed metric payload".to_vec();
        let entry = DeadLetter {
            kafka_topic: "telemetry.ingested".to_string(),
            kafka_partition: 2,
            kafka_offset: 42,
            payload: payload.clone(),
            stage: DeadLetterStage::Decode,
            error: "telemetry metrics must be set for a smart meter payload".to_string(),
        };

        publisher.park(&entry).await.unwrap();

        let sent = publisher.sink.sent.lock().unwrap();
        assert_eq!(sent.len(), 1);
        let record = &sent[0];
        assert_eq!(record.topic, "telemetry.ingested.dlq");
        assert_eq!(
            record.key.as_deref(),
            Some("telemetry.ingested:2:42".as_bytes())
        );
        assert_eq!(record.payload, payload);
        assert_eq!(
            record.headers.get("source_topic").map(Vec::as_slice),
            Some("telemetry.ingested".as_bytes())
        );
        assert_eq!(
            record.headers.get("source_partition").map(Vec::as_slice),
            Some("2".as_bytes())
        );
        assert_eq!(
            record.headers.get("source_offset").map(Vec::as_slice),
            Some("42".as_bytes())
        );
        assert_eq!(
            record.headers.get("stage").map(Vec::as_slice),
            Some("decode".as_bytes())
        );
        assert_eq!(
            record.headers.get("error").map(Vec::as_slice),
            Some("telemetry metrics must be set for a smart meter payload".as_bytes())
        );
        assert_eq!(metrics.snapshot().dlq_parked, 1);
    }

    #[tokio::test]
    async fn outbox_publisher_encodes_alert_resolved_json_as_protobuf() {
        let publisher = KafkaOutboxEventPublisher {
            sink: FakeSink::default(),
        };
        let event = OutboxEvent {
            event_id: Uuid::parse_str("5f1b98a9-7ab4-4bb7-8f24-90ae8b09a76c").unwrap(),
            claim_id: Uuid::new_v4(),
            topic: "alert.resolved".to_string(),
            event_type: OutboxEventType::AlertResolved,
            aggregate_id: "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b".to_string(),
            payload: include_bytes!(
                "../../../../../proto/fixtures/alert_resolved_outbox_payload.json"
            )
            .to_vec(),
        };

        publisher.publish(&event).await.unwrap();

        let sent = publisher.sink.sent.lock().unwrap();
        assert_eq!(sent.len(), 1);
        assert_eq!(sent[0].topic, "alert.resolved");
        assert_eq!(
            sent[0].key.as_deref(),
            Some(b"0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b".as_slice())
        );
        let payload = proto::AlertResolved::decode(sent[0].payload.as_slice()).unwrap();
        assert_eq!(payload.alert_id, "0f7b1d6c-2b4a-4f8e-9a1b-2c3d4e5f6a7b");
        assert_eq!(payload.asset_id, "battery-01");
        assert_eq!(payload.site_id, "site-01");
        assert_eq!(payload.severity, proto::Severity::Critical as i32);
        assert_eq!(payload.reason, "temperature exceeded threshold");
        assert_eq!(payload.opened_at_utc, 1789383600);
        assert_eq!(payload.resolved_at_utc, 1789387200);
        assert_eq!(payload.resolution_note, "fan cleaned");
        assert_eq!(payload.resolved_by, "actor-0101");
        assert_eq!(
            sent[0].headers.get("event_id").map(Vec::as_slice),
            Some(event.event_id.to_string().as_bytes())
        );
    }

    #[tokio::test]
    async fn outbox_publisher_encodes_alert_opened_json_as_protobuf() {
        let publisher = KafkaOutboxEventPublisher {
            sink: FakeSink::default(),
        };
        let event = OutboxEvent {
            event_id: Uuid::new_v4(),
            claim_id: Uuid::new_v4(),
            topic: "alert.opened".to_string(),
            event_type: OutboxEventType::AlertOpened,
            aggregate_id: "alert-0101".to_string(),
            payload: include_bytes!(
                "../../../../../proto/fixtures/alert_opened_outbox_payload.json"
            )
            .to_vec(),
        };

        publisher.publish(&event).await.unwrap();

        let sent = publisher.sink.sent.lock().unwrap();
        let payload = proto::AlertOpened::decode(sent[0].payload.as_slice()).unwrap();
        assert_eq!(payload.asset_id, "met-0101");
        assert_eq!(payload.severity, proto::Severity::Critical as i32);
        assert_eq!(
            payload.source_event_id.as_deref(),
            Some("telemetry.ingested:2:17")
        );
    }

    #[tokio::test]
    async fn outbox_publisher_rejects_unknown_event_types() {
        let publisher = KafkaOutboxEventPublisher {
            sink: FakeSink::default(),
        };
        let event = OutboxEvent {
            event_id: Uuid::new_v4(),
            claim_id: Uuid::new_v4(),
            topic: "payment.failed".to_string(),
            event_type: OutboxEventType::from_persisted("payment.failed".to_string()),
            aggregate_id: "payment-0101".to_string(),
            payload: b"{}".to_vec(),
        };

        let err = publisher.publish(&event).await.unwrap_err();

        assert!(
            err.to_string()
                .contains("unsupported outbox event type payment.failed")
        );
        assert!(publisher.sink.sent.lock().unwrap().is_empty());
    }

    #[tokio::test]
    async fn outbox_publisher_encodes_payment_confirmed_json_as_protobuf() {
        let publisher = KafkaOutboxEventPublisher {
            sink: FakeSink::default(),
        };
        let event = OutboxEvent {
            event_id: Uuid::new_v4(),
            claim_id: Uuid::new_v4(),
            topic: "payment.confirmed".to_string(),
            event_type: OutboxEventType::PaymentConfirmed,
            aggregate_id: "pay-0101".to_string(),
            payload: include_bytes!(
                "../../../../../proto/fixtures/payment_confirmed_outbox_payload.json"
            )
            .to_vec(),
        };

        publisher.publish(&event).await.unwrap();

        let sent = publisher.sink.sent.lock().unwrap();
        let payload = proto::PaymentConfirmed::decode(sent[0].payload.as_slice()).unwrap();
        assert_eq!(sent[0].topic, "payment.confirmed");
        assert_eq!(payload.payment_id, "pay-0101");
        assert_eq!(payload.amount_minor_units, 500000);
        assert_eq!(payload.confirmed_at_utc, 1790864940);
    }

    #[tokio::test]
    async fn outbox_publisher_encodes_credit_issued_json_as_protobuf() {
        let publisher = KafkaOutboxEventPublisher {
            sink: FakeSink::default(),
        };
        let event = OutboxEvent {
            event_id: Uuid::new_v4(),
            claim_id: Uuid::new_v4(),
            topic: "credit.issued".to_string(),
            event_type: OutboxEventType::CreditIssued,
            aggregate_id: "credit-0101".to_string(),
            payload: include_bytes!(
                "../../../../../proto/fixtures/credit_issued_outbox_payload.json"
            )
            .to_vec(),
        };

        publisher.publish(&event).await.unwrap();

        let sent = publisher.sink.sent.lock().unwrap();
        let payload = proto::CreditIssued::decode(sent[0].payload.as_slice()).unwrap();
        assert_eq!(sent[0].topic, "credit.issued");
        assert_eq!(payload.credit_id, "credit-0101");
        assert_eq!(payload.kwh_granted, 20.0);
        assert_eq!(payload.created_at_utc, 1790865000);
    }

    #[tokio::test]
    async fn outbox_publisher_encodes_meter_command_requested_json_as_protobuf() {
        let publisher = KafkaOutboxEventPublisher {
            sink: FakeSink::default(),
        };
        let event = OutboxEvent {
            event_id: Uuid::parse_str("a1b2c3d4-e5f6-7890-abcd-ef1234567890").unwrap(),
            claim_id: Uuid::new_v4(),
            topic: "meter.command.requested".to_string(),
            event_type: OutboxEventType::MeterCommandRequested,
            aggregate_id: "cmd-0101".to_string(),
            payload: include_bytes!(
                "../../../../../proto/fixtures/meter_command_requested_outbox_payload.json"
            )
            .to_vec(),
        };

        publisher.publish(&event).await.unwrap();

        let sent = publisher.sink.sent.lock().unwrap();
        assert_eq!(sent.len(), 1);
        assert_eq!(sent[0].topic, "meter.command.requested");
        assert_eq!(sent[0].key.as_deref(), Some(b"cmd-0101".as_slice()));
        let payload = proto::MeterCommandRequested::decode(sent[0].payload.as_slice()).unwrap();
        assert_eq!(payload.command_id, "cmd-0101");
        assert_eq!(payload.meter_id, "met-0101");
        assert_eq!(payload.site_id, "site-0101");
        assert_eq!(payload.assignment_id, "asgn-0101");
        assert_eq!(payload.command_type, "reconnect_meter");
        assert_eq!(payload.status, "requested");
        assert_eq!(payload.requested_by, "provider:paystack");
        assert_eq!(payload.reason, "balance recharged from zero");
        assert_eq!(payload.requested_at_utc, 1790865000);
        assert_eq!(
            sent[0].headers.get("event_id").map(Vec::as_slice),
            Some(event.event_id.to_string().as_bytes())
        );
    }

    #[tokio::test]
    async fn outbox_publisher_allows_custom_topics_for_known_event_types() {
        let publisher = KafkaOutboxEventPublisher {
            sink: FakeSink::default(),
        };
        let event = OutboxEvent {
            event_id: Uuid::new_v4(),
            claim_id: Uuid::new_v4(),
            topic: "custom.alert.opened".to_string(),
            event_type: OutboxEventType::AlertOpened,
            aggregate_id: "alert-0101".to_string(),
            payload: include_bytes!(
                "../../../../../proto/fixtures/alert_opened_outbox_payload.json"
            )
            .to_vec(),
        };

        publisher.publish(&event).await.unwrap();

        let sent = publisher.sink.sent.lock().unwrap();
        assert_eq!(sent[0].topic, "custom.alert.opened");
    }
}
