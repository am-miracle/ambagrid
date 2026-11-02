-- Edge delivery refuses a command after expires_at; a reconnect delivered
-- hours late may contradict what the operator wants now.
ALTER TABLE meter_commands ADD COLUMN expires_at timestamptz;

UPDATE meter_commands SET expires_at = requested_at + interval '24 hours';

ALTER TABLE meter_commands
    ALTER COLUMN expires_at SET DEFAULT now() + interval '24 hours',
    ALTER COLUMN expires_at SET NOT NULL,
    ADD CONSTRAINT meter_commands_expires_at_check CHECK (expires_at > requested_at);

-- Edge polling reads one site's undelivered commands in request order.
CREATE INDEX meter_commands_meter_pending_idx
    ON meter_commands (meter_id, requested_at, command_id)
    WHERE status IN ('requested', 'queued', 'sent');
