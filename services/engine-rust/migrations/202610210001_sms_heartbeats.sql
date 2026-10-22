CREATE TABLE sms_heartbeats (
    heartbeat_key text PRIMARY KEY,
    site_id text NOT NULL REFERENCES sites(site_id),
    gateway_id text NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 60 AND 86400),
    sent_at timestamptz NOT NULL,
    -- least(sent_at, first received_at): neither clock can overstate liveness.
    contact_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > contact_at),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, gateway_id, sequence)
);

CREATE INDEX sms_heartbeats_site_contact_idx ON sms_heartbeats (site_id, contact_at DESC);

CREATE TABLE sms_heartbeat_receipts (
    provider_message_id text PRIMARY KEY,
    heartbeat_key text NOT NULL REFERENCES sms_heartbeats(heartbeat_key),
    sender text NOT NULL,
    recipient text NOT NULL,
    received_at timestamptz NOT NULL,
    outbound_cost_minor bigint NOT NULL CHECK (outbound_cost_minor >= 0),
    inbound_cost_minor bigint NOT NULL CHECK (inbound_cost_minor >= 0),
    currency char(3) NOT NULL
);

CREATE INDEX sms_heartbeat_receipts_received_idx ON sms_heartbeat_receipts (received_at);
