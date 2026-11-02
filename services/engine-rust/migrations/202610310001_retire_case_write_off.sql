-- Unknown delivery must stay under investigation. Preserve the original
-- decision in audit history, reopen existing write-offs, then retire the action.
WITH prior AS MATERIALIZED (
    SELECT c.*,sm.site_id FROM command_reconciliation_cases c
    JOIN meter_commands mc USING(command_id)
    JOIN smart_meters sm USING(meter_id)
    WHERE c.disposition='write_off' FOR UPDATE OF c
), reopened AS (
    UPDATE command_reconciliation_cases c
    SET state='investigating',finding='unknown',disposition='investigate',
        resolved_by=NULL,needs_review=false,updated_at=now()
    FROM prior WHERE c.command_id=prior.command_id RETURNING c.command_id
)
INSERT INTO audit_events(site_id,actor_id,action,subject_type,subject_id,metadata)
SELECT prior.site_id,'system:reconciliation-policy','meter_command.reconciliation.reopened',
       'meter_command',prior.command_id::text,
       jsonb_build_object('reason','Write-off retired; delivery requires investigation','previous_case',to_jsonb(prior))
FROM prior JOIN reopened USING(command_id);

ALTER TABLE command_reconciliation_cases DROP CONSTRAINT command_reconciliation_cases_disposition_check;
ALTER TABLE command_reconciliation_cases ADD CONSTRAINT command_reconciliation_cases_disposition_check
    CHECK (disposition IN ('investigate','delivered','redeliver','refund'));
