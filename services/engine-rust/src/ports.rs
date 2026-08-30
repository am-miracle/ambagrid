use std::future::Future;

use chrono::{DateTime, Utc};
use uuid::Uuid;

use crate::domain::{
    alert::{Alert, AlertDecision},
    asset::{Asset, Reading},
};

#[derive(Debug, thiserror::Error)]
pub enum PortError {
    #[error("storage error: {0}")]
    Storage(#[source] Box<dyn std::error::Error + Send + Sync>),
    #[error("{0}")]
    Message(String),
}

impl PortError {
    pub fn storage(error: impl std::error::Error + Send + Sync + 'static) -> Self {
        Self::Storage(Box::new(error))
    }

    pub fn message(message: impl Into<String>) -> Self {
        Self::Message(message.into())
    }

    // Storage errors are assumed to be transient infra issues (a connection
    // blip, a stalled pool) worth retrying; Message errors are a
    // logic/data-shape problem retrying can't fix.
    pub fn is_retryable(&self) -> bool {
        matches!(self, Self::Storage(_))
    }
}

pub trait AssetRepository: Send + Sync {
    fn upsert_asset(&self, asset: &Asset) -> impl Future<Output = Result<(), PortError>> + Send;
    fn upsert_state(&self, reading: &Reading)
    -> impl Future<Output = Result<(), PortError>> + Send;
}

pub trait ReadingsSink: Send + Sync {
    fn append_reading(
        &self,
        reading: &Reading,
    ) -> impl Future<Output = Result<(), PortError>> + Send;
}

pub trait AlertRepository: Send + Sync {
    fn open_alert(
        &self,
        decision: &AlertDecision,
    ) -> impl Future<Output = Result<Alert, PortError>> + Send;

    // Most recently opened open alert for the asset, if any. Used to find
    // what to resolve when a reading comes back within normal range.
    fn find_open_alert(
        &self,
        asset_id: &str,
    ) -> impl Future<Output = Result<Option<Alert>, PortError>> + Send;

    fn resolve_alert(
        &self,
        alert_id: Uuid,
        resolved_at: DateTime<Utc>,
        resolution_note: &str,
        resolved_by: &str,
    ) -> impl Future<Output = Result<Alert, PortError>> + Send;
}

// The ontology's business-event stream (see docs/ontology.md's Event
// Topics section): alert.opened and alert.resolved, published alongside
// the Postgres writes that make those states durable.
pub trait AlertEvents: Send + Sync {
    fn alert_opened(&self, alert: &Alert) -> impl Future<Output = Result<(), PortError>> + Send;
    fn alert_resolved(&self, alert: &Alert) -> impl Future<Output = Result<(), PortError>> + Send;
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DeadLetterStage {
    Decode,
    Ingest,
}

impl DeadLetterStage {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Decode => "decode",
            Self::Ingest => "ingest",
        }
    }
}

// A telemetry message that failed to decode or ingest, preserved for
// inspection or replay instead of being dropped.
pub struct DeadLetter {
    pub kafka_topic: String,
    pub kafka_partition: i32,
    pub kafka_offset: i64,
    pub payload: Vec<u8>,
    pub stage: DeadLetterStage,
    pub error: String,
}

pub trait DeadLetterSink: Send + Sync {
    fn park(&self, entry: &DeadLetter) -> impl Future<Output = Result<(), PortError>> + Send;
}
