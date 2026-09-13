CREATE INDEX CONCURRENTLY IF NOT EXISTS alerts_open_opened_at_id_idx
    ON alerts (opened_at DESC, alert_id DESC)
    WHERE status = 'open';

CREATE INDEX CONCURRENTLY IF NOT EXISTS alerts_opened_at_id_idx
    ON alerts (opened_at DESC, alert_id DESC);
