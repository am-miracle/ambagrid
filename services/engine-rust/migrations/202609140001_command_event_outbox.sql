CREATE TABLE command_event_outbox (
    event_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    topic text NOT NULL,
    event_type text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    payload jsonb NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    claim_id uuid,
    claimed_at timestamptz,
    published_at timestamptz
);

CREATE INDEX command_event_outbox_unpublished_recorded_at_idx
    ON command_event_outbox (recorded_at, event_id)
    WHERE published_at IS NULL;

CREATE INDEX command_event_outbox_published_at_idx
    ON command_event_outbox (published_at)
    WHERE published_at IS NOT NULL;
