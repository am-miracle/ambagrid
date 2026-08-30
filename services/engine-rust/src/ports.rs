use std::future::Future;

use crate::domain::{
    alert::{Alert, AlertDecision},
    asset::Reading,
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

// What the threshold policy decided a reading should do to alert state.
// Computed purely (see ThresholdPolicy::evaluate/recovered) before any I/O.
pub enum PolicyOutcome {
    Open(AlertDecision),
    Resolve {
        kind: String,
        resolution_note: String,
        resolved_by: String,
    },
    Unchanged,
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct IngestWrite {
    pub alert_opened: Option<Alert>,
    pub alert_resolved: Option<Alert>,
}

// One reading's entire write side: asset state, the time-series reading, and
// whatever alert-state change the policy decided on. A real implementation
// runs this as a single transaction, so a mid-write failure (and the retry
// it triggers) can never leave a partial commit — e.g. a reading recorded
// without its alert decision, or recorded twice.
pub trait IngestRepository: Send + Sync {
    fn ingest(
        &self,
        reading: &Reading,
        outcome: PolicyOutcome,
    ) -> impl Future<Output = Result<IngestWrite, PortError>> + Send;
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
