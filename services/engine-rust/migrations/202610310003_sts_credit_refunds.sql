-- Records an externally completed refund for an STS credit that was never vended.
-- Vending and this refund exclude each other through the funding check below.
CREATE TABLE sts_credit_refunds (
    credit_id UUID PRIMARY KEY REFERENCES energy_credits(credit_id),
    payment_id UUID NOT NULL UNIQUE REFERENCES payments(payment_id),
    reference TEXT NOT NULL UNIQUE CHECK (length(trim(reference)) > 0),
    actor TEXT NOT NULL CHECK (length(trim(actor)) > 0),
    evidence TEXT NOT NULL CHECK (length(trim(evidence)) > 0),
    kwh NUMERIC(18,6) NOT NULL CHECK (kwh >= 0),
    money_minor BIGINT NOT NULL CHECK (money_minor >= 0),
    shortfall_kwh NUMERIC(18,6) NOT NULL CHECK (shortfall_kwh >= 0),
    shortfall_money_minor BIGINT NOT NULL CHECK (shortfall_money_minor >= 0),
    refunded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION sts_funding_block_reason(
    vend_reference text, funding_payment_id uuid, vend_meter_id text,
    vend_amount_minor bigint, vend_installation text
) RETURNS text LANGUAGE plpgsql STABLE AS $$
DECLARE funding record;
BEGIN
    IF funding_payment_id IS NULL OR vend_reference IS DISTINCT FROM funding_payment_id::text THEN
        RETURN 'noncanonical_payment_reference';
    END IF;
    SELECT p.status, p.amount_minor_units, p.currency, ec.delivery_mode,
        sm.sts_meter_number, sm.sts_installation, sm.sts_currency, tp.minor_units_per_major
    INTO funding
    FROM payments p JOIN energy_credits ec USING (payment_id)
    JOIN meter_assignments ma USING (assignment_id)
    JOIN smart_meters sm ON sm.meter_id=ma.meter_id
    JOIN tariff_plans tp ON tp.tariff_plan_id=ec.tariff_plan_id
    WHERE p.payment_id=funding_payment_id;
    IF NOT FOUND THEN
        RETURN 'funding_not_found';
    ELSIF EXISTS(SELECT 1 FROM sts_credit_refunds WHERE payment_id=funding_payment_id) THEN
        RETURN 'credit_refunded';
    ELSIF funding.status <> 'confirmed' THEN
        RETURN 'payment_' || funding.status;
    ELSIF funding.delivery_mode <> 'sts' THEN
        RETURN 'credit_not_sts';
    ELSIF funding.amount_minor_units <> vend_amount_minor THEN
        RETURN 'payment_amount_changed';
    ELSIF funding.sts_meter_number IS DISTINCT FROM vend_meter_id THEN
        RETURN 'meter_configuration_changed';
    ELSIF funding.sts_installation IS DISTINCT FROM vend_installation THEN
        RETURN 'installation_changed';
    ELSIF funding.sts_currency IS DISTINCT FROM funding.currency THEN
        RETURN 'currency_changed';
    ELSIF funding.minor_units_per_major <> 100 THEN
        RETURN 'unsupported_currency_precision';
    END IF;
    RETURN NULL;
END;
$$;
