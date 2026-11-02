-- Closes a credit that can be neither redelivered nor refunded, moving no money.
ALTER TABLE command_reconciliation_cases DROP CONSTRAINT command_reconciliation_cases_disposition_check;
ALTER TABLE command_reconciliation_cases ADD CONSTRAINT command_reconciliation_cases_disposition_check
    CHECK (disposition IN ('investigate','delivered','redeliver','refund','write_off'));
