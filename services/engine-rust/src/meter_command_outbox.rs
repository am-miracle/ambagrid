//! JSON shapes persisted by the API in `command_event_outbox`.
//! The publisher validates them before converting them to protobuf.

use serde::Deserialize;

#[derive(Debug, Clone, PartialEq, Deserialize)]
pub struct MeterCommandRequestedPayload {
    pub command_id: String,
    pub meter_id: String,
    pub site_id: String,
    pub assignment_id: String,
    pub command_type: String,
    pub status: String,
    pub requested_by: String,
    pub reason: String,
    pub requested_at: String,
    // Absent from rows recorded before command expiry and credit amounts existed.
    #[serde(default)]
    pub expires_at: Option<String>,
    #[serde(default)]
    pub credit_kwh: Option<f64>,
    #[serde(default)]
    pub credit_id: Option<String>,
}

/// Shared by `meter.command.acknowledged` and `meter.command.failed`.
#[derive(Debug, Clone, PartialEq, Deserialize)]
pub struct MeterCommandOutcomePayload {
    pub command_id: String,
    pub meter_id: String,
    pub site_id: String,
    pub command_type: String,
    pub status: String,
    pub reported_by: String,
    pub failure_reason: Option<String>,
    pub occurred_at: String,
    pub credit_kwh: Option<f64>,
}
