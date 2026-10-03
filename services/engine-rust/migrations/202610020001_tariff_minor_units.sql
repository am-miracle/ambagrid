-- Existing tariffs use two-decimal currencies. Persisting the scale makes the
-- conversion explicit and allows future zero- or three-decimal currencies.
ALTER TABLE tariff_plans
    ADD COLUMN minor_units_per_major integer NOT NULL DEFAULT 100;

ALTER TABLE tariff_plans
    ADD CONSTRAINT tariff_plans_minor_units_check CHECK (minor_units_per_major > 0);

CREATE OR REPLACE FUNCTION protect_tariff_plan_calculation_fields()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF ROW(
        NEW.tariff_plan_id,
        NEW.site_id,
        NEW.currency,
        NEW.price_per_kwh,
        NEW.standing_charge_minor_units,
        NEW.effective_from,
        NEW.minor_units_per_major
    ) IS DISTINCT FROM ROW(
        OLD.tariff_plan_id,
        OLD.site_id,
        OLD.currency,
        OLD.price_per_kwh,
        OLD.standing_charge_minor_units,
        OLD.effective_from,
        OLD.minor_units_per_major
    ) THEN
        RAISE EXCEPTION 'tariff calculation fields are immutable; create a new tariff plan version'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;
