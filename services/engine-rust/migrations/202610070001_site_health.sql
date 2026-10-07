CREATE TABLE site_health (
    site_id                 text PRIMARY KEY REFERENCES sites(site_id) ON DELETE CASCADE,
    gateway_id              text NOT NULL,
    last_contact_at         timestamptz NOT NULL,
    last_event_timestamp    timestamptz,
    queue_depth             bigint NOT NULL DEFAULT 0 CHECK (queue_depth >= 0),
    oldest_pending_at       timestamptz,
    queue_growing           boolean NOT NULL DEFAULT false,
    CONSTRAINT site_health_pending_consistent CHECK (
        (queue_depth = 0 AND oldest_pending_at IS NULL)
        OR (queue_depth > 0 AND oldest_pending_at IS NOT NULL)
    )
);

CREATE INDEX site_health_last_contact_idx ON site_health(last_contact_at);
