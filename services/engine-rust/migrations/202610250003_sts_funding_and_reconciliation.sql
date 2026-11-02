ALTER TABLE smart_meters DROP CONSTRAINT smart_meters_credit_mode_check;
ALTER TABLE smart_meters
    ADD CONSTRAINT smart_meters_credit_mode_check CHECK (credit_mode IN ('platform', 'meter', 'sts')),
    ADD COLUMN sts_meter_number TEXT,
    ADD COLUMN sts_installation TEXT,
    ADD COLUMN sts_currency TEXT,
    ADD CONSTRAINT smart_meters_sts_config_check CHECK (credit_mode <> 'sts' OR (
        sts_meter_number IS NOT NULL AND sts_meter_number ~ '^([0-9]{11}|[0-9]{16,18}|[0-9]{35})$'
        AND sts_installation IS NOT NULL AND sts_installation <> ''
        AND sts_currency IS NOT NULL AND sts_currency ~ '^[A-Z]{3}$'));

-- Snapshot the selected delivery path at credit issuance. Changing a meter's
-- current mode must not make an old automatically-delivered payment vendable.
ALTER TABLE energy_credits ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'platform'
    CHECK (delivery_mode IN ('platform', 'meter', 'sts'));
UPDATE energy_credits ec SET delivery_mode='meter'
WHERE EXISTS (SELECT 1 FROM meter_commands mc WHERE mc.credit_id=ec.credit_id);
CREATE FUNCTION stamp_credit_delivery_mode() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.delivery_mode IS DISTINCT FROM OLD.delivery_mode THEN
            RAISE EXCEPTION 'credit delivery mode is immutable';
        END IF;
    ELSE
        SELECT sm.credit_mode INTO NEW.delivery_mode
        FROM meter_assignments ma JOIN smart_meters sm USING (meter_id)
        WHERE ma.assignment_id=NEW.assignment_id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER energy_credits_delivery_mode BEFORE INSERT OR UPDATE OF delivery_mode
    ON energy_credits FOR EACH ROW EXECUTE FUNCTION stamp_credit_delivery_mode();

-- Preserve legacy unlinked reservations for reconciliation, but never permit
-- a new unfunded vend. A payment gets only one canonical vending reference.
ALTER TABLE sts_vends ADD COLUMN payment_id UUID UNIQUE REFERENCES payments(payment_id),
    ADD COLUMN not_issued_at TIMESTAMPTZ,
    ADD CONSTRAINT sts_vends_resolution_check CHECK (not_issued_at IS NULL OR result IS NULL);
CREATE FUNCTION guard_sts_funding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE funding RECORD;
BEGIN
    IF NEW.payment_id IS NULL OR NEW.reference <> NEW.payment_id::text THEN
        RAISE EXCEPTION 'STS vending requires a canonical payment reference';
    END IF;
    SELECT p.status,p.amount_minor_units,p.currency,ec.delivery_mode,
        sm.sts_meter_number,sm.sts_installation,sm.sts_currency,tp.minor_units_per_major
    INTO funding
    FROM payments p JOIN energy_credits ec USING (payment_id)
    JOIN meter_assignments ma USING (assignment_id)
    JOIN smart_meters sm ON sm.meter_id=ma.meter_id
    JOIN tariff_plans tp ON tp.tariff_plan_id=ec.tariff_plan_id
    WHERE p.payment_id=NEW.payment_id;
    IF NOT FOUND OR funding.status <> 'confirmed' OR funding.delivery_mode <> 'sts'
       OR funding.amount_minor_units <> NEW.amount_minor
       OR funding.sts_meter_number IS DISTINCT FROM NEW.meter_id
       OR funding.sts_installation IS DISTINCT FROM NEW.installation
       OR funding.sts_currency IS DISTINCT FROM funding.currency
       OR funding.minor_units_per_major <> 100 THEN
        RAISE EXCEPTION 'payment is not eligible for this STS vend';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER sts_vends_funding BEFORE INSERT ON sts_vends FOR EACH ROW EXECUTE FUNCTION guard_sts_funding();

CREATE FUNCTION guard_sts_command_credit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.command_type='credit_meter' AND (
        EXISTS(SELECT 1 FROM smart_meters WHERE meter_id=NEW.meter_id AND credit_mode='sts')
        OR EXISTS(SELECT 1 FROM energy_credits WHERE credit_id=NEW.credit_id AND delivery_mode='sts')) THEN
        RAISE EXCEPTION 'STS credit cannot also be delivered as a kWh command';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER meter_commands_sts_credit BEFORE INSERT OR UPDATE OF command_type,credit_id,meter_id ON meter_commands
    FOR EACH ROW EXECUTE FUNCTION guard_sts_command_credit();

CREATE TABLE sts_vend_reconciliations (
    reconciliation_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    reference TEXT NOT NULL REFERENCES sts_vends(reference),
    actor TEXT NOT NULL CHECK (length(trim(actor)) > 0),
    evidence TEXT NOT NULL CHECK (length(trim(evidence)) > 0),
    outcome TEXT NOT NULL CHECK (outcome IN ('recovered', 'not_issued')),
    reconciled_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
