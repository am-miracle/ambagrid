use uuid::Uuid;

#[derive(Debug, Clone, PartialEq, Eq)]
#[allow(dead_code)]
pub struct ResolveAlertInput {
    pub alert_id: Uuid,
    pub resolution_note: String,
    pub resolved_by: String,
}
