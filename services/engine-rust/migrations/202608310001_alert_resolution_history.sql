CREATE TABLE alert_resolution_history (
    resolution_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    alert_id uuid NOT NULL REFERENCES alerts(alert_id),
    resolved_at timestamptz NOT NULL,
    resolution_note text NOT NULL,
    resolved_by text NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX alert_resolution_history_alert_id_recorded_at_idx
    ON alert_resolution_history (alert_id, recorded_at DESC);
