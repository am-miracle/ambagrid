-- Safe retry is allowed only when the HTTP transport proved no request was sent.
-- The canonical funded reservation survives; it is rearmed before the next call.
ALTER TABLE sts_vends ADD COLUMN safe_to_retry BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN last_error TEXT,
    ADD CONSTRAINT sts_vends_retry_state_check CHECK (
        NOT safe_to_retry OR (result IS NULL AND not_issued_at IS NULL));

-- Keep the original credit/payment history and record an exact compensating
-- entry. A terminal provider non-issuance is not itself a payment refund.
CREATE TABLE sts_credit_reversals (
    reference TEXT PRIMARY KEY REFERENCES sts_vends(reference),
    credit_id UUID NOT NULL UNIQUE REFERENCES energy_credits(credit_id),
    kwh NUMERIC(18,6) NOT NULL CHECK (kwh > 0),
    money_minor BIGINT NOT NULL CHECK (money_minor >= 0),
    actor TEXT NOT NULL CHECK (length(trim(actor)) > 0),
    evidence TEXT NOT NULL CHECK (length(trim(evidence)) > 0),
    reversed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A safely retryable request is still subject to its funding eligibility when
-- rearmed. Refunds/configuration changes during an outage must not be bypassed.
CREATE TRIGGER sts_vends_retry_funding BEFORE UPDATE OF safe_to_retry ON sts_vends
    FOR EACH ROW WHEN (OLD.safe_to_retry AND NOT NEW.safe_to_retry
        AND NEW.not_issued_at IS NULL AND NEW.result IS NULL)
    EXECUTE FUNCTION guard_sts_funding();
