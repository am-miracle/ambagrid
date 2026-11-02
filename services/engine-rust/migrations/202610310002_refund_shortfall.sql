-- A verified refund reverses what the balance still holds. The remainder is a
-- recorded discrepancy, so a consumed balance no longer blocks the refund.
ALTER TABLE meter_credit_refunds
    ADD COLUMN shortfall_kwh numeric(18,6) NOT NULL DEFAULT 0 CHECK (shortfall_kwh >= 0),
    ADD COLUMN shortfall_money_minor bigint NOT NULL DEFAULT 0 CHECK (shortfall_money_minor >= 0),
    DROP CONSTRAINT meter_credit_refunds_kwh_check,
    ADD CONSTRAINT meter_credit_refunds_kwh_check CHECK (kwh >= 0);

-- A command with no case row is the open state; no writer ever stored 'open'.
ALTER TABLE command_reconciliation_cases DROP CONSTRAINT command_reconciliation_cases_state_check;
ALTER TABLE command_reconciliation_cases ADD CONSTRAINT command_reconciliation_cases_state_check
    CHECK (state IN ('investigating','resolved'));
