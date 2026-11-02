-- Reserve a vending identity before HTTP transmission. An unresolved row is
-- deliberately retained for operator reconciliation, never automatically resent.
CREATE TABLE sts_vends (
    reference TEXT PRIMARY KEY CHECK (reference ~ '^[a-zA-Z0-9_.,-]{1,40}$'),
    installation TEXT NOT NULL CHECK (installation <> ''),
    meter_id TEXT NOT NULL,
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    result JSONB,
    completed_at TIMESTAMPTZ,
    CHECK ((result IS NULL) = (completed_at IS NULL))
);
