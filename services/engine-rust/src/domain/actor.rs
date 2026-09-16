#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub struct ActorId(String);

impl ActorId {
    pub fn new(value: impl Into<String>) -> Result<Self, ActorIdError> {
        let value = value.into();
        let trimmed = value.trim();
        if trimmed.is_empty() {
            Err(ActorIdError::Empty)
        } else if trimmed == "system" {
            Err(ActorIdError::ReservedSystemActor)
        } else {
            Ok(Self(trimmed.to_string()))
        }
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum ActorIdError {
    #[error("actor_id must not be empty")]
    Empty,
    #[error("actor_id must not be the reserved system actor")]
    ReservedSystemActor,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ResolutionActor {
    Human(ActorId),
    System,
}

impl ResolutionActor {
    pub fn as_str(&self) -> &str {
        match self {
            Self::Human(actor_id) => actor_id.as_str(),
            Self::System => "system",
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn actor_id_trims_input() {
        let actor_id = ActorId::new(" actor-0101 ").unwrap();

        assert_eq!(actor_id.as_str(), "actor-0101");
    }

    #[test]
    fn actor_id_rejects_empty_input() {
        let err = ActorId::new(" ").unwrap_err();

        assert_eq!(err, ActorIdError::Empty);
    }

    #[test]
    fn actor_id_rejects_reserved_system_actor() {
        let err = ActorId::new("system").unwrap_err();

        assert_eq!(err, ActorIdError::ReservedSystemActor);
    }
}
