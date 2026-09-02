ALTER TABLE telemetry_dead_letters
    ADD COLUMN stage text NOT NULL DEFAULT 'decode';

ALTER TABLE telemetry_dead_letters
    ADD CONSTRAINT telemetry_dead_letters_stage_check
        CHECK (stage IN ('decode', 'ingest'));
