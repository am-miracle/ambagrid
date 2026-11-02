-- Keeps undelivered meter credits in the reconciliation listing without
-- scanning command history.
CREATE INDEX meter_commands_undelivered_credit_idx
    ON meter_commands (requested_at, command_id)
    WHERE command_type = 'credit_meter' AND status IN ('failed', 'expired');

-- Finds funded STS credits with no vend.
CREATE INDEX energy_credits_sts_payment_idx
    ON energy_credits (created_at, credit_id)
    WHERE delivery_mode = 'sts' AND payment_id IS NOT NULL;

-- NULL on reservations made before vends recorded their operator.
ALTER TABLE sts_vends ADD COLUMN requested_by TEXT;
