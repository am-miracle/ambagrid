use std::{collections::BTreeMap, future::Future};

use prost::Message;
use rdkafka::{
    ClientConfig,
    message::{Header, OwnedHeaders},
    producer::{FutureProducer, FutureRecord},
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

// Publishes the alert.opened/alert.resolved business events described in
// docs/ontology.md.
pub struct KafkaAlertEventPublisher<S: RecordSink = FutureProducer> {
    sink: S,
    alert_opened_topic: String,
    alert_resolved_topic: String,
}

pub struct KafkaDeadLetterPublisher<S: RecordSink = FutureProducer> {
    topic: String,
    sink: S,
}

impl KafkaAlertEventPublisher<FutureProducer> {
    pub fn connect(config: ProducerConfig) -> Result<Self, rdkafka::error::KafkaError> {
        let sink = ClientConfig::new()
            .set("bootstrap.servers", config.brokers.join(","))
            .set("message.timeout.ms", "5000")
            .create()?;

        Ok(Self {
            sink,
            alert_opened_topic: config.alert_opened_topic,
            alert_resolved_topic: config.alert_resolved_topic,
        })
    }
}

impl KafkaDeadLetterPublisher<FutureProducer> {
    pub fn connect(
        brokers: Vec<String>,
        topic: String,
    ) -> Result<Self, rdkafka::error::KafkaError> {
        let sink = ClientConfig::new()
            .set("bootstrap.servers", brokers.join(","))
            .set("message.timeout.ms", "5000")
            .create()?;

        Ok(Self { topic, sink })
    }
}

impl<S: RecordSink> AlertEvents for KafkaAlertEventPublisher<S> {
    async fn alert_opened(&self, alert: &Alert) -> Result<(), PortError> {
        self.sink
            .send(kafka_record(
                &self.alert_opened_topic,
                None,
                alert_opened_event(alert).encode_to_vec(),
                BTreeMap::new(),
            ))
            .await
    }

    async fn alert_resolved(&self, alert: &Alert) -> Result<(), PortError> {
        self.sink
            .send(kafka_record(
                &self.alert_resolved_topic,
                None,
                alert_resolved_event(alert)?.encode_to_vec(),
                BTreeMap::new(),
            ))
            .await
    }
}

impl<S: RecordSink> DeadLetterSink for KafkaDeadLetterPublisher<S> {
    async fn park(&self, entry: &DeadLetter) -> Result<(), PortError> {
        self.sink.send(dead_letter_record(&self.topic, entry)).await
    }
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
        source_event_id: alert.source_event_id.clone(),
    }
}

fn alert_resolved_event(alert: &Alert) -> Result<proto::AlertResolved, PortError> {
    let resolved_at = alert.resolved_at.ok_or_else(|| {
        PortError::message(format!(
            "resolved alert {} is missing resolved_at",
            alert.alert_id
        ))
    })?;

    Ok(proto::AlertResolved {
        alert_id: alert.alert_id.to_string(),
        asset_id: alert.asset_id.clone(),
        site_id: alert.site_id.clone(),
        severity: to_proto_severity(alert.severity) as i32,
        reason: alert.reason.clone(),
        opened_at_utc: alert.opened_at.timestamp(),
        resolved_at_utc: resolved_at.timestamp(),
        resolution_note: alert.resolution_note.clone().unwrap_or_default(),
        resolved_by: alert
            .resolved_by
            .as_ref()
            .map(|actor| actor.as_str().to_string())
            .unwrap_or_default(),
    })
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use chrono::{Duration, Utc};
    use prost::Message;
    use uuid::Uuid;

    use super::*;
    use crate::domain::{
        alert::{AlertKind, AlertStatus, Severity},
        operator::ResolutionActor,
    };
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
            sink: FakeSink::default(),
            alert_opened_topic: "alert.opened".to_string(),
            alert_resolved_topic: "alert.resolved".to_string(),
        };
        let alert = open_alert();

        publisher.alert_opened(&alert).await.unwrap();

        let sent = publisher.sink.sent.lock().unwrap();
        assert_eq!(sent.len(), 1);
        assert_eq!(sent[0].topic, "alert.opened");
        assert!(sent[0].key.is_none());
        assert!(sent[0].headers.is_empty());

        let event = proto::AlertOpened::decode(sent[0].payload.as_slice()).unwrap();
        assert_eq!(event.alert_id, alert.alert_id.to_string());
        assert_eq!(event.asset_id, "met-0101");
        assert_eq!(event.site_id, "ng-kaji-01");
        assert_eq!(event.severity, proto::Severity::Critical as i32);
        assert_eq!(event.reason, alert.reason);
        assert_eq!(event.opened_at_utc, alert.opened_at.timestamp());
        assert_eq!(event.source_event_id.as_deref(), Some("telemetry-evt-0101"));
    }

    #[tokio::test]
    async fn dead_letter_publisher_preserves_payload_and_source_metadata() {
        let publisher = KafkaDeadLetterPublisher {
            topic: "telemetry.ingested.dlq".to_string(),
            sink: FakeSink::default(),
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
    }

    #[tokio::test]
    async fn alert_resolved_publishes_only_to_the_resolved_topic_sink() {
        let publisher = KafkaAlertEventPublisher {
            sink: FakeSink::default(),
            alert_opened_topic: "alert.opened".to_string(),
            alert_resolved_topic: "alert.resolved".to_string(),
        };
        let mut alert = open_alert();
        alert.status = AlertStatus::Resolved;
        alert.resolved_at = Some(alert.opened_at + Duration::minutes(5));
        alert.resolution_note =
            Some("internal_temperature_recovered:65.0C<threshold:65.0C".to_string());
        alert.resolved_by = Some(ResolutionActor::System);

        publisher.alert_resolved(&alert).await.unwrap();

        let sent = publisher.sink.sent.lock().unwrap();
        assert_eq!(sent.len(), 1);
        assert_eq!(sent[0].topic, "alert.resolved");
        assert!(sent[0].key.is_none());
        assert!(sent[0].headers.is_empty());

        let event = proto::AlertResolved::decode(sent[0].payload.as_slice()).unwrap();
        assert_eq!(event.alert_id, alert.alert_id.to_string());
        assert_eq!(
            event.resolved_at_utc,
            alert.resolved_at.unwrap().timestamp()
        );
        assert_eq!(event.resolution_note, alert.resolution_note.unwrap());
        assert_eq!(event.resolved_by, "system");
    }

    #[test]
    fn alert_resolved_event_rejects_missing_resolved_at() {
        let alert = Alert {
            status: AlertStatus::Resolved,
            resolved_at: None,
            ..open_alert()
        };

        let err = alert_resolved_event(&alert).unwrap_err();

        assert_eq!(
            err.to_string(),
            format!("resolved alert {} is missing resolved_at", alert.alert_id)
        );
    }

    #[test]
    fn alert_resolved_event_uses_the_alert_resolved_at_when_present() {
        let resolved_at = Utc::now() - Duration::minutes(5);
        let alert = Alert {
            status: AlertStatus::Resolved,
            resolved_at: Some(resolved_at),
            ..open_alert()
        };

        let event = alert_resolved_event(&alert).unwrap();

        assert_eq!(event.resolved_at_utc, resolved_at.timestamp());
    }
}
