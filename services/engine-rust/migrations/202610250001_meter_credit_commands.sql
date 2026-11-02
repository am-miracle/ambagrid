-- 'platform' meters are relay-controlled against the balance AmbaGrid holds;
-- 'meter' meters keep their own balance and receive each credit as a command.
ALTER TABLE smart_meters
    ADD COLUMN credit_mode text NOT NULL DEFAULT 'platform',
    ADD CONSTRAINT smart_meters_credit_mode_check CHECK (credit_mode IN ('platform', 'meter'));

ALTER TABLE meter_commands
    ADD COLUMN credit_kwh numeric(18, 6),
    ADD COLUMN credit_id uuid REFERENCES energy_credits(credit_id);

-- NOT VALID: credit commands created before amounts existed are left as they
-- are; every new or updated row must carry an amount exactly when it credits.
ALTER TABLE meter_commands
    ADD CONSTRAINT meter_commands_credit_amount_check CHECK (
        (command_type = 'credit_meter') = (credit_kwh IS NOT NULL)
        AND (credit_kwh IS NULL OR credit_kwh > 0)
    ) NOT VALID;

-- One energy credit is delivered to a meter at most once.
CREATE UNIQUE INDEX meter_commands_credit_id_idx
    ON meter_commands (credit_id)
    WHERE credit_id IS NOT NULL;
