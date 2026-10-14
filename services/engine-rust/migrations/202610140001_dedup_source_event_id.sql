ALTER TABLE dedup_log ADD COLUMN source_event_id TEXT;

UPDATE dedup_log
SET source_event_id = 'edge:' || site_id || ':' || sequence;

ALTER TABLE dedup_log ALTER COLUMN source_event_id SET NOT NULL;
ALTER TABLE dedup_log DROP CONSTRAINT dedup_log_pkey;
ALTER TABLE dedup_log ADD PRIMARY KEY (source_event_id);
ALTER TABLE dedup_log DROP COLUMN site_id, DROP COLUMN sequence;
