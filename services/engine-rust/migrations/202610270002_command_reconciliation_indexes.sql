-- The reconciliation listing starts from these two small sets, so it never
-- scans a site's full command history.
CREATE INDEX audit_events_command_conflict_idx
    ON audit_events (site_id, subject_id)
    WHERE subject_type = 'meter_command' AND action = 'meter_command.report_conflict';

-- Every expiry api-go writes now records previous_status, so this index stops
-- growing: it holds only expiries from code that also expired queued commands.
CREATE INDEX audit_events_legacy_command_expiry_idx
    ON audit_events (site_id, subject_id)
    WHERE subject_type = 'meter_command' AND action = 'meter_command.expired'
      AND actor_id = 'system:command-expiry' AND NOT (metadata ? 'previous_status');
