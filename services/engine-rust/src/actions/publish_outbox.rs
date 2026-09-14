use std::time::Duration;

use crate::ports::{MarkPublishedOutcome, OutboxEvents, OutboxRepository, PortError};

pub struct PublishOutbox<'a, R, E> {
    pub store: &'a R,
    pub events: &'a E,
    pub batch_size: i64,
}

impl<'a, R, E> PublishOutbox<'a, R, E>
where
    R: OutboxRepository,
    E: OutboxEvents,
{
    pub fn new(store: &'a R, events: &'a E, batch_size: i64) -> Self {
        Self {
            store,
            events,
            batch_size,
        }
    }

    pub async fn publish_once(&self) -> Result<usize, PortError> {
        if self.batch_size < 1 {
            return Err(PortError::message("batch_size must be greater than zero"));
        }

        let events = self.store.claim_unpublished(self.batch_size).await?;
        let mut published = 0;
        let mut first_error = None;
        for event in events {
            if let Err(error) = self.events.publish(&event).await {
                tracing::warn!(
                    event_id = %event.event_id,
                    error = %error,
                    "failed to publish outbox event"
                );
                if first_error.is_none() {
                    first_error = Some(error);
                }
                continue;
            }
            let outcome = match self
                .store
                .mark_published(event.event_id, event.claim_id)
                .await
            {
                Ok(outcome) => outcome,
                Err(error) => {
                    tracing::warn!(
                        event_id = %event.event_id,
                        error = %error,
                        "failed to mark outbox event as published"
                    );
                    if first_error.is_none() {
                        first_error = Some(error);
                    }
                    continue;
                }
            };
            if outcome == MarkPublishedOutcome::ClaimLost {
                tracing::info!(
                    event_id = %event.event_id,
                    "outbox event was published after its claim expired"
                );
            }
            published += 1;
        }
        if let Some(error) = first_error {
            return Err(error);
        }
        Ok(published)
    }

    pub async fn prune_published(&self, retention: Duration) -> Result<u64, PortError> {
        self.store.prune_published(retention).await
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use uuid::Uuid;

    use crate::ports::{
        MarkPublishedOutcome, OutboxEvent, OutboxEvents, OutboxRepository, PortError,
    };

    use super::*;

    #[derive(Default)]
    struct FakeOutboxRepository {
        claimed: Mutex<Vec<OutboxEvent>>,
        seen_limit: Mutex<Option<i64>>,
        marked: Mutex<Vec<Uuid>>,
        lose_claim: bool,
    }

    impl OutboxRepository for FakeOutboxRepository {
        async fn claim_unpublished(&self, limit: i64) -> Result<Vec<OutboxEvent>, PortError> {
            *self.seen_limit.lock().unwrap() = Some(limit);
            Ok(self.claimed.lock().unwrap().clone())
        }

        async fn mark_published(
            &self,
            event_id: Uuid,
            _claim_id: Uuid,
        ) -> Result<MarkPublishedOutcome, PortError> {
            if self.lose_claim {
                return Ok(MarkPublishedOutcome::ClaimLost);
            }
            self.marked.lock().unwrap().push(event_id);
            Ok(MarkPublishedOutcome::Marked)
        }

        async fn prune_published(&self, _retention: Duration) -> Result<u64, PortError> {
            Ok(0)
        }
    }

    #[derive(Default)]
    struct FakeOutboxEvents {
        published: Mutex<Vec<Uuid>>,
        fail: Mutex<Vec<Uuid>>,
    }

    impl OutboxEvents for FakeOutboxEvents {
        async fn publish(&self, event: &OutboxEvent) -> Result<(), PortError> {
            if self.fail.lock().unwrap().contains(&event.event_id) {
                return Err(PortError::message("broker unavailable"));
            }
            self.published.lock().unwrap().push(event.event_id);
            Ok(())
        }
    }

    fn event() -> OutboxEvent {
        OutboxEvent {
            event_id: Uuid::new_v4(),
            claim_id: Uuid::new_v4(),
            topic: "alert.resolved".to_string(),
            event_type: "alert.resolved".to_string(),
            aggregate_id: "alert-1".to_string(),
            payload: br#"{"alert_id":"alert-1"}"#.to_vec(),
        }
    }

    #[tokio::test]
    async fn publishes_claimed_events_and_marks_them_published() {
        let first = event();
        let second = event();
        let store = FakeOutboxRepository {
            claimed: Mutex::new(vec![first.clone(), second.clone()]),
            ..Default::default()
        };
        let events = FakeOutboxEvents::default();
        let action = PublishOutbox::new(&store, &events, 25);

        let published = action.publish_once().await.unwrap();

        assert_eq!(published, 2);
        assert_eq!(*store.seen_limit.lock().unwrap(), Some(25));
        assert_eq!(
            *events.published.lock().unwrap(),
            vec![first.event_id, second.event_id]
        );
        assert_eq!(
            *store.marked.lock().unwrap(),
            vec![first.event_id, second.event_id]
        );
    }

    #[tokio::test]
    async fn leaves_unmarked_events_for_retry_when_publish_fails() {
        let event = event();
        let store = FakeOutboxRepository {
            claimed: Mutex::new(vec![event.clone()]),
            ..Default::default()
        };
        let events = FakeOutboxEvents {
            published: Mutex::default(),
            fail: Mutex::new(vec![event.event_id]),
        };
        let action = PublishOutbox::new(&store, &events, 25);

        let err = action.publish_once().await.unwrap_err();

        assert_eq!(err.to_string(), "broker unavailable");
        assert!(store.marked.lock().unwrap().is_empty());
    }

    #[tokio::test]
    async fn continues_through_the_batch_after_a_publish_failure() {
        let failed = event();
        let next = event();
        let store = FakeOutboxRepository {
            claimed: Mutex::new(vec![failed.clone(), next.clone()]),
            ..Default::default()
        };
        let events = FakeOutboxEvents {
            published: Mutex::default(),
            fail: Mutex::new(vec![failed.event_id]),
        };
        let action = PublishOutbox::new(&store, &events, 25);

        let err = action.publish_once().await.unwrap_err();

        assert_eq!(err.to_string(), "broker unavailable");
        assert_eq!(*events.published.lock().unwrap(), vec![next.event_id]);
        assert_eq!(*store.marked.lock().unwrap(), vec![next.event_id]);
    }

    #[tokio::test]
    async fn treats_an_expired_claim_as_a_duplicate_safe_publish() {
        let store = FakeOutboxRepository {
            claimed: Mutex::new(vec![event()]),
            lose_claim: true,
            ..Default::default()
        };
        let events = FakeOutboxEvents::default();
        let action = PublishOutbox::new(&store, &events, 25);

        let published = action.publish_once().await.unwrap();

        assert_eq!(published, 1);
        assert_eq!(events.published.lock().unwrap().len(), 1);
        assert!(store.marked.lock().unwrap().is_empty());
    }

    #[tokio::test]
    async fn rejects_empty_batches() {
        let store = FakeOutboxRepository::default();
        let events = FakeOutboxEvents::default();
        let action = PublishOutbox::new(&store, &events, 0);

        let err = action.publish_once().await.unwrap_err();

        assert_eq!(err.to_string(), "batch_size must be greater than zero");
    }
}
