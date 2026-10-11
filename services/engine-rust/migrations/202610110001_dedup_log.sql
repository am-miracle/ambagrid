CREATE TABLE IF NOT EXISTS dedup_log (
    site_id    TEXT        NOT NULL,
    sequence   BIGINT      NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, sequence)
);

CREATE INDEX idx_dedup_log_created_at ON dedup_log (created_at);
