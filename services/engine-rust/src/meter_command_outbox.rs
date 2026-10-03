//! JSON shape persisted by the API in `command_event_outbox`.
//! The publisher validates it before converting it to protobuf.

use serde::Deserialize;

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
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
}
