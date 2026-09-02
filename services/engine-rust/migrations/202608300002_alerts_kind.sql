ALTER TABLE alerts
    ADD COLUMN kind text;

-- Existing alerts on this branch were opened only by the initial internal
-- temperature threshold policy. Backfill them before making the column
-- mandatory so databases with pre-existing alert rows can migrate safely.
UPDATE alerts
SET kind = 'internal_temperature'
WHERE kind IS NULL;

ALTER TABLE alerts
    ALTER COLUMN kind SET NOT NULL;

-- Enforced in Postgres, not just app-level check-then-act: at most one open
-- alert per (asset_id, kind), so distinct problems on the same asset can
-- have independent alerts, and concurrent ingests can't race past the
-- application's duplicate-open check.
CREATE UNIQUE INDEX alerts_asset_id_kind_open_uidx
    ON alerts (asset_id, kind)
    WHERE status = 'open';
