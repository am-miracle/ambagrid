ALTER TABLE alerts
    ADD COLUMN resolution_note text,
    ADD COLUMN resolved_by text;

ALTER TABLE alerts
    ADD CONSTRAINT alerts_resolved_by_check
        CHECK (
            (status = 'open' AND resolved_by IS NULL)
            OR (status = 'resolved' AND resolved_by IS NOT NULL)
        );
