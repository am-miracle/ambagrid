ALTER TABLE site_health
    ADD COLUMN last_successful_sync_at timestamptz,
    ADD COLUMN last_failed_sync_reason text;
