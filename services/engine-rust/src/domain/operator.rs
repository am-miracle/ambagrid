#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub struct OperatorId(String);

impl OperatorId {
    pub fn new(value: impl Into<String>) -> Result<Self, OperatorIdError> {
        let value = value.into();
        let trimmed = value.trim();
        if trimmed.is_empty() {
            Err(OperatorIdError::Empty)
        } else if trimmed == "system" {
            Err(OperatorIdError::ReservedSystemActor)
        } else {
            Ok(Self(trimmed.to_string()))
        }
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum OperatorIdError {
    #[error("operator_id must not be empty")]
    Empty,
    #[error("operator_id must not be the reserved system actor")]
    ReservedSystemActor,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ResolutionActor {
    Operator(OperatorId),
    System,
}

impl ResolutionActor {
    pub fn as_str(&self) -> &str {
        match self {
            Self::Operator(operator_id) => operator_id.as_str(),
            Self::System => "system",
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn operator_id_trims_input() {
        let operator_id = OperatorId::new(" operator-0101 ").unwrap();

        assert_eq!(operator_id.as_str(), "operator-0101");
    }

    #[test]
    fn operator_id_rejects_empty_input() {
        let err = OperatorId::new(" ").unwrap_err();

        assert_eq!(err, OperatorIdError::Empty);
    }

    #[test]
    fn operator_id_rejects_reserved_system_actor() {
        let err = OperatorId::new("system").unwrap_err();

        assert_eq!(err, OperatorIdError::ReservedSystemActor);
    }
}
