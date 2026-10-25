-- Non-issuance reverses what the balance holds; the rest is a shortfall to
-- investigate. Reverting the kwh >= 0 check fails once a zero reversal exists.
ALTER TABLE sts_credit_reversals
    DROP CONSTRAINT sts_credit_reversals_kwh_check,
    ADD CONSTRAINT sts_credit_reversals_kwh_check CHECK (kwh >= 0),
    ADD COLUMN shortfall_kwh NUMERIC(18,6) NOT NULL DEFAULT 0 CHECK (shortfall_kwh >= 0),
    ADD COLUMN shortfall_money_minor BIGINT NOT NULL DEFAULT 0 CHECK (shortfall_money_minor >= 0);

CREATE INDEX sts_credit_reversals_shortfall_idx
    ON sts_credit_reversals (reversed_at, reference)
    WHERE shortfall_kwh > 0 OR shortfall_money_minor > 0;
