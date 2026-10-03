//! JSON shapes persisted by the API in `command_event_outbox`.
//! The publisher validates these shapes before converting them to protobuf.

use serde::Deserialize;

#[derive(Debug, Clone, PartialEq, Deserialize)]
pub struct PaymentConfirmedPayload {
    pub payment_id: String,
    pub provider: String,
    pub external_reference: String,
    pub customer_id: String,
    pub amount_minor_units: i64,
    pub currency: String,
    pub confirmed_at: String,
}

#[derive(Debug, Clone, PartialEq, Deserialize)]
pub struct CreditIssuedPayload {
    pub credit_id: String,
    pub site_id: String,
    pub assignment_id: String,
    pub payment_id: String,
    pub tariff_plan_id: String,
    pub source_type: String,
    pub source_id: String,
    pub kwh_granted: f64,
    pub money_value_minor_units: i64,
    pub created_at: String,
}
