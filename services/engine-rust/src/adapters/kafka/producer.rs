use std::{collections::BTreeMap, future::Future};

use chrono::Utc;
use prost::Message;
use rskafka::client::{
    ClientBuilder,
    partition::{Compression, PartitionClient, UnknownTopicHandling},
};

use super::proto;
use crate::{
    domain::alert::{Alert, Severity},
    ports::{AlertEvents, DeadLetter, DeadLetterSink, PortError},
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProducerConfig {
    pub brokers: Vec<String>,
    pub alert_opened_topic: String,
    pub alert_resolved_topic: String,
}

// A single-record producer, kept as a trait so tests can substitute a fake
// and assert on topic routing/payloads without a live broker.
pub(crate) trait RecordSink: Send + Sync {
    fn send(&self, payload: Vec<u8>) -> impl Future<Output = Result<(), PortError>> + Send;
}

impl RecordSink for PartitionClient {
    async fn send(&self, payload: Vec<u8>) -> Result<(), PortError> {
        let record = rskafka::record::Record {
            key: None,
            value: Some(payload),
            headers: BTreeMap::new(),
            timestamp: Utc::now(),
        };

        self.produce(vec![record], Compression::NoCompression)
            .await
            .map_err(PortError::storage)?;

        Ok(())
    }
}

// Publishes the alert.opened/alert.resolved business events described in
// docs/ontology.md. Single-partition, same assumption as the telemetry
// consumer: fine for one engine replica.
pub struct KafkaAlertEventPublisher<S: RecordSink = PartitionClient> {
    alert_opened: S,
    alert_resolved: S,
}

pub struct KafkaDeadLetterPublisher<S: DeadLetterRecordSink = PartitionClient> {
    sink: S,
}

impl KafkaAlertEventPublisher<PartitionClient> {
    pub async fn connect(config: ProducerConfig) -> Result<Self, rskafka::client::error::Error> {
        let client = ClientBuilder::new(config.brokers).build().await?;
        let alert_opened = client
            .partition_client(config.alert_opened_topic, 0, UnknownTopicHandling::Retry)
            .await?;
        let alert_resolved = client
            .partition_client(config.alert_resolved_topic, 0, UnknownTopicHandling::Retry)
            .await?;

        Ok(Self {
            alert_opened,
            alert_resolved,
        })
    }
}

impl KafkaDeadLetterPublisher<PartitionClient> {
    pub async fn connect(
        brokers: Vec<String>,
        topic: String,
    ) -> Result<Self, rskafka::client::error::Error> {
        let client = ClientBuilder::new(brokers).build().await?;
        let sink = client
            .partition_client(topic.clone(), 0, UnknownTopicHandling::Retry)
            .await?;

        Ok(Self { sink })
    }
}

pub(crate) trait DeadLetterRecordSink: Send + Sync {
    fn send_record(
        &self,
        record: rskafka::record::Record,
    ) -> impl Future<Output = Result<(), PortError>> + Send;
}

impl DeadLetterRecordSink for PartitionClient {
    async fn send_record(&self, record: rskafka::record::Record) -> Result<(), PortError> {
        self.produce(vec![record], Compression::NoCompression)
            .await
            .map_err(PortError::storage)?;

        Ok(())
    }
}

impl<S: RecordSink> AlertEvents for KafkaAlertEventPublisher<S> {
    async fn alert_opened(&self, alert: &Alert) -> Result<(), PortError> {
        self.alert_opened
            .send(alert_opened_event(alert).encode_to_vec())
            .await
    }

    async fn alert_resolved(&self, alert: &Alert) -> Result<(), PortError> {
        self.alert_resolved
            .send(alert_resolved_event(alert).encode_to_vec())
            .await
    }
}

impl<S: DeadLetterRecordSink> DeadLetterSink for KafkaDeadLetterPublisher<S> {
    async fn park(&self, entry: &DeadLetter) -> Result<(), PortError> {
        self.sink.send_record(dead_letter_record(entry)).await
    }
}

fn dead_letter_record(entry: &DeadLetter) -> rskafka::record::Record {
    rskafka::record::Record {
        key: Some(
            format!(
                "{}:{}:{}",
                entry.kafka_topic, entry.kafka_partition, entry.kafka_offset
            )
            .into_bytes(),
        ),
        value: Some(entry.payload.clone()),
        headers: BTreeMap::from([
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
        timestamp: Utc::now(),
    }
}

fn to_proto_severity(severity: Severity) -> proto::Severity {
    match severity {
        Severity::Info => proto::Severity::Info,
        Severity::Warning => proto::Severity::Warning,
        Severity::Critical => proto::Severity::Critical,
    }
}

fn alert_opened_event(alert: &Alert) -> proto::AlertOpened {
    proto::AlertOpened {
        alert_id: alert.alert_id.to_string(),
        asset_id: alert.asset_id.clone(),
        site_id: alert.site_id.clone(),
        severity: to_proto_severity(alert.severity) as i32,
        reason: alert.reason.clone(),
        opened_at_utc: alert.opened_at.timestamp(),
        kind: alert.kind.as_str().to_string(),
        source_event_id: alert.source_event_id.clone(),
    }
}

// `resolved_at` is expected to always be set on a resolved alert (the
// Postgres schema enforces it), but the domain type doesn't guarantee that
// statically — falling back to "now" keeps a malformed `Alert` from
// producing a garbage timestamp on the wire.
fn alert_resolved_event(alert: &Alert) -> proto::AlertResolved {
    proto::AlertResolved {
        alert_id: alert.alert_id.to_string(),
        asset_id: alert.asset_id.clone(),
        site_id: alert.site_id.clone(),
        severity: to_proto_severity(alert.severity) as i32,
        reason: alert.reason.clone(),
        opened_at_utc: alert.opened_at.timestamp(),
        resolved_at_utc: alert.resolved_at.unwrap_or_else(Utc::now).timestamp(),
        resolution_note: alert.resolution_note.clone().unwrap_or_default(),
        resolved_by: alert.resolved_by.clone().unwrap_or_default(),
        kind: alert.kind.as_str().to_string(),
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use chrono::{Duration, Utc};
    use prost::Message;
    use uuid::Uuid;

    use super::*;
    use crate::domain::alert::{AlertKind, AlertStatus, Severity};
    use crate::ports::DeadLetterStage;

    #[derive(Default)]
    struct FakeSink {
        sent: Mutex<Vec<Vec<u8>>>,
    }

    impl RecordSink for FakeSink {
        async fn send(&self, payload: Vec<u8>) -> Result<(), PortError> {
            self.sent.lock().unwrap().push(payload);
            Ok(())
        }
    }

    #[derive(Default)]
    struct FakeDeadLetterSink {
        sent: Mutex<Vec<rskafka::record::Record>>,
    }

    impl DeadLetterRecordSink for FakeDeadLetterSink {
        async fn send_record(&self, record: rskafka::record::Record) -> Result<(), PortError> {
            self.sent.lock().unwrap().push(record);
            Ok(())
        }
    }

    fn open_alert() -> Alert {
        Alert {
            alert_id: Uuid::new_v4(),
            asset_id: "met-0101".to_string(),
            site_id: "ng-kaji-01".to_string(),
            kind: AlertKind::from("internal_temperature"),
            severity: Severity::Critical,
            status: AlertStatus::Open,
            reason: "internal_temperature_high:72.4C>=threshold:70.0C".to_string(),
            opened_at: Utc::now(),
            source_event_id: Some("telemetry-evt-0101".to_string()),
            resolved_at: None,
            resolution_note: None,
            resolved_by: None,
        }
    }

    #[tokio::test]
    async fn alert_opened_publishes_only_to_the_opened_topic_sink() {
        let publisher = KafkaAlertEventPublisher {
            alert_opened: FakeSink::default(),
            alert_resolved: FakeSink::default(),
        };
        let alert = open_alert();

        publisher.alert_opened(&alert).await.unwrap();

        let opened_sent = publisher.alert_opened.sent.lock().unwrap();
        assert_eq!(opened_sent.len(), 1);
        assert!(publisher.alert_resolved.sent.lock().unwrap().is_empty());

        let event = proto::AlertOpened::decode(opened_sent[0].as_slice()).unwrap();
        assert_eq!(event.alert_id, alert.alert_id.to_string());
        assert_eq!(event.asset_id, "met-0101");
        assert_eq!(event.site_id, "ng-kaji-01");
        assert_eq!(event.severity, proto::Severity::Critical as i32);
        assert_eq!(event.reason, alert.reason);
        assert_eq!(event.opened_at_utc, alert.opened_at.timestamp());
        assert_eq!(event.kind, "internal_temperature");
        assert_eq!(event.source_event_id.as_deref(), Some("telemetry-evt-0101"));
    }

    #[tokio::test]
    async fn dead_letter_publisher_preserves_payload_and_source_metadata() {
        let publisher = KafkaDeadLetterPublisher {
            sink: FakeDeadLetterSink::default(),
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
        assert_eq!(
            record.key.as_deref(),
            Some("telemetry.ingested:2:42".as_bytes())
        );
        assert_eq!(record.value.as_deref(), Some(payload.as_slice()));
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
    }

    #[tokio::test]
    async fn alert_resolved_publishes_only_to_the_resolved_topic_sink() {
        let publisher = KafkaAlertEventPublisher {
            alert_opened: FakeSink::default(),
            alert_resolved: FakeSink::default(),
        };
        let mut alert = open_alert();
        alert.status = AlertStatus::Resolved;
        alert.resolved_at = Some(alert.opened_at + Duration::minutes(5));
        alert.resolution_note =
            Some("internal_temperature_recovered:65.0C<threshold:65.0C".to_string());
        alert.resolved_by = Some("system".to_string());

        publisher.alert_resolved(&alert).await.unwrap();

        let resolved_sent = publisher.alert_resolved.sent.lock().unwrap();
        assert_eq!(resolved_sent.len(), 1);
        assert!(publisher.alert_opened.sent.lock().unwrap().is_empty());

        let event = proto::AlertResolved::decode(resolved_sent[0].as_slice()).unwrap();
        assert_eq!(event.alert_id, alert.alert_id.to_string());
        assert_eq!(
            event.resolved_at_utc,
            alert.resolved_at.unwrap().timestamp()
        );
        assert_eq!(event.resolution_note, alert.resolution_note.unwrap());
        assert_eq!(event.resolved_by, "system");
    }

    #[test]
    fn alert_resolved_event_falls_back_to_now_when_resolved_at_is_missing() {
        let alert = Alert {
            status: AlertStatus::Resolved,
            resolved_at: None,
            ..open_alert()
        };

        let before = Utc::now().timestamp();
        let event = alert_resolved_event(&alert);
        let after = Utc::now().timestamp();

        assert!((before..=after).contains(&event.resolved_at_utc));
    }

    #[test]
    fn alert_resolved_event_uses_the_alert_resolved_at_when_present() {
        let resolved_at = Utc::now() - Duration::minutes(5);
        let alert = Alert {
            status: AlertStatus::Resolved,
            resolved_at: Some(resolved_at),
            ..open_alert()
        };

        let event = alert_resolved_event(&alert);

        assert_eq!(event.resolved_at_utc, resolved_at.timestamp());
    }
}
