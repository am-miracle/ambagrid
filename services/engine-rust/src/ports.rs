use std::{future::Future, time::Duration};

use chrono::{DateTime, Utc};
use uuid::Uuid;

use crate::domain::{
    actor::{ActorId, ResolutionActor},
    alert::{Alert, AlertDecision, AlertKind},
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
        kind: AlertKind,
        resolution_note: String,
        resolved_by: ResolutionActor,
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

pub trait AlertRepository: Send + Sync {
    fn resolve_alert(
        &self,
        alert_id: Uuid,
        resolved_at: DateTime<Utc>,
        resolution_note: &str,
        resolved_by: ResolutionActor,
    ) -> impl Future<Output = Result<Alert, PortError>> + Send;
}

pub trait ResolveAlertPermission: Send + Sync {
    fn authorize_resolve_alert(
        &self,
        actor_id: &ActorId,
    ) -> impl Future<Output = Result<(), PortError>> + Send;
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DeadLetterStage {
    Decode,
}

impl DeadLetterStage {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Decode => "decode",
        }
    }
}

// A telemetry message that failed to decode or validate before it became a
// domain reading, preserved for inspection or replay instead of being dropped.
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

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct OutboxEvent {
    pub event_id: Uuid,
    pub claim_id: Uuid,
    pub topic: String,
    pub event_type: OutboxEventType,
    pub aggregate_id: String,
    pub payload: Vec<u8>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum OutboxEventType {
    AlertOpened,
    AlertResolved,
    Unsupported(String),
}

impl OutboxEventType {
    pub fn from_persisted(value: String) -> Self {
        match value.as_str() {
            "alert.opened" => Self::AlertOpened,
            "alert.resolved" => Self::AlertResolved,
            _ => Self::Unsupported(value),
        }
    }

    pub fn as_str(&self) -> &str {
        match self {
            Self::AlertOpened => "alert.opened",
            Self::AlertResolved => "alert.resolved",
            Self::Unsupported(value) => value,
        }
    }
}

impl std::fmt::Display for OutboxEventType {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str(self.as_str())
    }
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum MarkPublishedOutcome {
    #[default]
    Marked,
    ClaimLost,
}

pub trait OutboxRepository: Send + Sync {
    fn claim_unpublished(
        &self,
        limit: i64,
    ) -> impl Future<Output = Result<Vec<OutboxEvent>, PortError>> + Send;

    fn mark_published(
        &self,
        event_id: Uuid,
        claim_id: Uuid,
    ) -> impl Future<Output = Result<MarkPublishedOutcome, PortError>> + Send;

    fn prune_published(
        &self,
        retention: Duration,
    ) -> impl Future<Output = Result<u64, PortError>> + Send;
}

pub trait OutboxEvents: Send + Sync {
    fn publish(&self, event: &OutboxEvent) -> impl Future<Output = Result<(), PortError>> + Send;
}
