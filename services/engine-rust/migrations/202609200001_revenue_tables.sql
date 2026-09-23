ALTER TABLE assets
    ADD CONSTRAINT assets_asset_id_asset_type_site_unique
    UNIQUE (asset_id, asset_type, site_id);

CREATE TABLE customers (
    customer_id text PRIMARY KEY,
    site_id text NOT NULL REFERENCES sites(site_id),
    display_name text NOT NULL,
    phone_number text,
    status text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT customers_customer_id_check CHECK (customer_id <> ''),
    CONSTRAINT customers_display_name_check CHECK (display_name <> ''),
    CONSTRAINT customers_phone_number_check CHECK (phone_number IS NULL OR phone_number <> ''),
    CONSTRAINT customers_status_check CHECK (status IN ('active', 'inactive', 'suspended')),
    CONSTRAINT customers_customer_id_site_unique UNIQUE (customer_id, site_id)
);

CREATE INDEX customers_site_id_status_idx
    ON customers (site_id, status);

CREATE TRIGGER customers_set_updated_at
    BEFORE UPDATE ON customers
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE households (
    household_id text PRIMARY KEY,
    site_id text NOT NULL REFERENCES sites(site_id),
    customer_id text,
    display_name text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT households_household_id_check CHECK (household_id <> ''),
    CONSTRAINT households_display_name_check CHECK (display_name <> ''),
    CONSTRAINT households_status_check CHECK (status IN ('active', 'inactive', 'disconnected')),
    CONSTRAINT households_customer_site_fk
        FOREIGN KEY (customer_id, site_id)
        REFERENCES customers(customer_id, site_id),
    CONSTRAINT households_household_id_site_unique UNIQUE (household_id, site_id)
);

CREATE INDEX households_site_id_status_idx
    ON households (site_id, status);

CREATE INDEX households_customer_id_idx
    ON households (customer_id)
    WHERE customer_id IS NOT NULL;

CREATE TRIGGER households_set_updated_at
    BEFORE UPDATE ON households
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE smart_meters (
    meter_id text PRIMARY KEY,
    site_id text NOT NULL REFERENCES sites(site_id),
    household_id text,
    asset_id text NOT NULL,
    asset_type text NOT NULL DEFAULT 'smart_meter',
    status text NOT NULL DEFAULT 'active',
    installed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT smart_meters_meter_id_check CHECK (meter_id <> ''),
    CONSTRAINT smart_meters_asset_type_check CHECK (asset_type = 'smart_meter'),
    CONSTRAINT smart_meters_status_check CHECK (status IN ('active', 'inactive', 'disconnected', 'retired')),
    CONSTRAINT smart_meters_asset_unique UNIQUE (asset_id),
    CONSTRAINT smart_meters_meter_site_unique UNIQUE (meter_id, site_id),
    CONSTRAINT smart_meters_household_site_fk
        FOREIGN KEY (household_id, site_id)
        REFERENCES households(household_id, site_id),
    CONSTRAINT smart_meters_asset_fk
        FOREIGN KEY (asset_id, asset_type, site_id)
        REFERENCES assets(asset_id, asset_type, site_id)
);

CREATE INDEX smart_meters_site_id_status_idx
    ON smart_meters (site_id, status);

CREATE INDEX smart_meters_household_id_idx
    ON smart_meters (household_id)
    WHERE household_id IS NOT NULL;

CREATE TRIGGER smart_meters_set_updated_at
    BEFORE UPDATE ON smart_meters
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE meter_assignments (
    assignment_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id text NOT NULL REFERENCES sites(site_id),
    meter_id text NOT NULL,
    customer_id text NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- now() is transaction-scoped, so opening and closing in one transaction
    -- gives an assignment equal timestamps. That correction is legitimate.
    CONSTRAINT meter_assignments_period_check CHECK (
        ended_at IS NULL OR ended_at >= started_at
    ),
    CONSTRAINT meter_assignments_assignment_site_unique UNIQUE (assignment_id, site_id),
    CONSTRAINT meter_assignments_meter_site_fk
        FOREIGN KEY (meter_id, site_id)
        REFERENCES smart_meters(meter_id, site_id),
    CONSTRAINT meter_assignments_customer_site_fk
        FOREIGN KEY (customer_id, site_id)
        REFERENCES customers(customer_id, site_id)
);

-- Relaxing this index is how split billing lets several customers share a meter.
CREATE UNIQUE INDEX meter_assignments_open_per_meter_idx
    ON meter_assignments (meter_id)
    WHERE ended_at IS NULL;

CREATE INDEX meter_assignments_customer_id_started_at_idx
    ON meter_assignments (customer_id, started_at DESC);

CREATE INDEX meter_assignments_site_id_meter_id_idx
    ON meter_assignments (site_id, meter_id);

CREATE FUNCTION protect_meter_assignment_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF ROW(
        NEW.assignment_id,
        NEW.site_id,
        NEW.meter_id,
        NEW.customer_id,
        NEW.started_at
    ) IS DISTINCT FROM ROW(
        OLD.assignment_id,
        OLD.site_id,
        OLD.meter_id,
        OLD.customer_id,
        OLD.started_at
    ) THEN
        RAISE EXCEPTION 'meter assignment identity is immutable; close this assignment and open a new one'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER meter_assignments_protect_identity
    BEFORE UPDATE ON meter_assignments
    FOR EACH ROW
    EXECUTE FUNCTION protect_meter_assignment_identity();

CREATE TABLE tariff_plans (
    tariff_plan_id text PRIMARY KEY,
    site_id text NOT NULL REFERENCES sites(site_id),
    name text NOT NULL,
    currency text NOT NULL,
    price_per_kwh numeric(18, 6) NOT NULL,
    standing_charge_minor_units bigint NOT NULL DEFAULT 0,
    effective_from timestamptz NOT NULL,
    effective_to timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tariff_plans_tariff_plan_id_check CHECK (tariff_plan_id <> ''),
    CONSTRAINT tariff_plans_name_check CHECK (name <> ''),
    CONSTRAINT tariff_plans_currency_check CHECK (currency <> ''),
    CONSTRAINT tariff_plans_price_per_kwh_check CHECK (price_per_kwh > 0),
    CONSTRAINT tariff_plans_standing_charge_check CHECK (standing_charge_minor_units >= 0),
    CONSTRAINT tariff_plans_effective_range_check CHECK (
        effective_to IS NULL OR effective_to > effective_from
    ),
    CONSTRAINT tariff_plans_tariff_plan_id_site_unique UNIQUE (tariff_plan_id, site_id)
);

CREATE INDEX tariff_plans_site_id_effective_from_idx
    ON tariff_plans (site_id, effective_from DESC);

CREATE TRIGGER tariff_plans_set_updated_at
    BEFORE UPDATE ON tariff_plans
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE FUNCTION protect_tariff_plan_calculation_fields()
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
        NEW.effective_from
    ) IS DISTINCT FROM ROW(
        OLD.tariff_plan_id,
        OLD.site_id,
        OLD.currency,
        OLD.price_per_kwh,
        OLD.standing_charge_minor_units,
        OLD.effective_from
    ) THEN
        RAISE EXCEPTION 'tariff calculation fields are immutable; create a new tariff plan version'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER tariff_plans_protect_calculation_fields
    BEFORE UPDATE ON tariff_plans
    FOR EACH ROW
    EXECUTE FUNCTION protect_tariff_plan_calculation_fields();

CREATE TABLE payments (
    payment_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    external_reference text NOT NULL,
    customer_id text NOT NULL REFERENCES customers(customer_id),
    amount_minor_units bigint NOT NULL,
    currency text NOT NULL,
    status text NOT NULL,
    confirmed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payments_provider_check CHECK (provider <> ''),
    CONSTRAINT payments_external_reference_check CHECK (external_reference <> ''),
    CONSTRAINT payments_amount_check CHECK (amount_minor_units > 0),
    CONSTRAINT payments_currency_check CHECK (currency <> ''),
    CONSTRAINT payments_status_check CHECK (status IN ('pending', 'confirmed', 'failed', 'reversed')),
    CONSTRAINT payments_confirmed_at_check CHECK (
        (status = 'confirmed' AND confirmed_at IS NOT NULL)
        OR (status <> 'confirmed')
    ),
    CONSTRAINT payments_provider_reference_unique UNIQUE (provider, external_reference),
    CONSTRAINT payments_payment_id_customer_unique UNIQUE (payment_id, customer_id)
);

CREATE INDEX payments_customer_id_confirmed_at_idx
    ON payments (customer_id, confirmed_at DESC)
    WHERE confirmed_at IS NOT NULL;

CREATE TRIGGER payments_set_updated_at
    BEFORE UPDATE ON payments
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE energy_credits (
    credit_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id text NOT NULL REFERENCES sites(site_id),
    assignment_id uuid NOT NULL,
    payment_id uuid,
    tariff_plan_id text,
    source_type text NOT NULL,
    source_id text NOT NULL,
    kwh_granted numeric(18, 6) NOT NULL,
    money_value_minor_units bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT energy_credits_source_type_check CHECK (
        source_type IN (
            'payment',
            'adjustment',
            'promotion',
            'operator_correction',
            'emergency_credit'
        )
    ),
    CONSTRAINT energy_credits_source_id_check CHECK (source_id <> ''),
    CONSTRAINT energy_credits_kwh_granted_check CHECK (kwh_granted > 0),
    CONSTRAINT energy_credits_money_value_check CHECK (
        money_value_minor_units IS NULL OR money_value_minor_units >= 0
    ),
    CONSTRAINT energy_credits_payment_source_check CHECK (
        (source_type = 'payment') = (payment_id IS NOT NULL)
    ),
    CONSTRAINT energy_credits_payment_tariff_check CHECK (
        source_type <> 'payment' OR tariff_plan_id IS NOT NULL
    ),
    CONSTRAINT energy_credits_credit_source_type_unique UNIQUE (credit_id, source_type),
    CONSTRAINT energy_credits_assignment_site_fk
        FOREIGN KEY (assignment_id, site_id)
        REFERENCES meter_assignments(assignment_id, site_id),
    CONSTRAINT energy_credits_payment_fk
        FOREIGN KEY (payment_id)
        REFERENCES payments(payment_id),
    CONSTRAINT energy_credits_tariff_site_fk
        FOREIGN KEY (tariff_plan_id, site_id)
        REFERENCES tariff_plans(tariff_plan_id, site_id)
);

CREATE FUNCTION check_energy_credit_payer()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.payment_id IS NOT NULL AND NOT EXISTS (
        SELECT 1
        FROM payments p
        JOIN meter_assignments a ON a.assignment_id = NEW.assignment_id
        WHERE p.payment_id = NEW.payment_id
          AND p.customer_id = a.customer_id
    ) THEN
        RAISE EXCEPTION 'energy credit payment belongs to a different customer than its assignment'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER energy_credits_check_payer
    BEFORE INSERT OR UPDATE ON energy_credits
    FOR EACH ROW
    EXECUTE FUNCTION check_energy_credit_payer();

CREATE INDEX energy_credits_assignment_id_created_at_idx
    ON energy_credits (assignment_id, created_at DESC);

CREATE INDEX energy_credits_site_id_created_at_idx
    ON energy_credits (site_id, created_at DESC);

CREATE INDEX energy_credits_payment_id_idx
    ON energy_credits (payment_id)
    WHERE payment_id IS NOT NULL;

CREATE INDEX energy_credits_tariff_plan_id_idx
    ON energy_credits (tariff_plan_id)
    WHERE tariff_plan_id IS NOT NULL;

CREATE TABLE credit_balances (
    assignment_id uuid PRIMARY KEY REFERENCES meter_assignments(assignment_id),
    remaining_kwh numeric(18, 6) NOT NULL DEFAULT 0,
    remaining_money_value_minor_units bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT credit_balances_remaining_kwh_check CHECK (remaining_kwh >= 0),
    CONSTRAINT credit_balances_remaining_money_check CHECK (remaining_money_value_minor_units >= 0)
);

CREATE TRIGGER credit_balances_set_updated_at
    BEFORE UPDATE ON credit_balances
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE FUNCTION protect_unsettled_credit_balance()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.remaining_kwh > 0 OR OLD.remaining_money_value_minor_units > 0 THEN
        RAISE EXCEPTION 'credit balance must be settled or transferred to zero before removal'
            USING ERRCODE = '23514';
    END IF;

    RETURN OLD;
END;
$$;

CREATE TRIGGER credit_balances_protect_unsettled
    BEFORE DELETE ON credit_balances
    FOR EACH ROW
    EXECUTE FUNCTION protect_unsettled_credit_balance();

CREATE TABLE emergency_credit_advances (
    advance_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id text NOT NULL REFERENCES sites(site_id),
    assignment_id uuid NOT NULL,
    credit_id uuid NOT NULL,
    source_type text NOT NULL DEFAULT 'emergency_credit',
    tariff_plan_id text NOT NULL,
    advanced_kwh numeric(18, 6) NOT NULL,
    advanced_minor_units bigint NOT NULL,
    outstanding_kwh numeric(18, 6) NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT now(),
    settled_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT emergency_credit_advances_source_type_check CHECK (source_type = 'emergency_credit'),
    CONSTRAINT emergency_credit_advances_advanced_kwh_check CHECK (advanced_kwh > 0),
    CONSTRAINT emergency_credit_advances_advanced_money_check CHECK (advanced_minor_units >= 0),
    CONSTRAINT emergency_credit_advances_outstanding_check CHECK (
        outstanding_kwh >= 0 AND outstanding_kwh <= advanced_kwh
    ),
    CONSTRAINT emergency_credit_advances_settled_check CHECK (
        (settled_at IS NOT NULL) = (outstanding_kwh = 0)
    ),
    CONSTRAINT emergency_credit_advances_credit_unique UNIQUE (credit_id),
    CONSTRAINT emergency_credit_advances_credit_fk
        FOREIGN KEY (credit_id, source_type)
        REFERENCES energy_credits(credit_id, source_type),
    CONSTRAINT emergency_credit_advances_assignment_site_fk
        FOREIGN KEY (assignment_id, site_id)
        REFERENCES meter_assignments(assignment_id, site_id),
    CONSTRAINT emergency_credit_advances_tariff_site_fk
        FOREIGN KEY (tariff_plan_id, site_id)
        REFERENCES tariff_plans(tariff_plan_id, site_id)
);

CREATE INDEX emergency_credit_advances_unsettled_idx
    ON emergency_credit_advances (assignment_id, issued_at)
    WHERE settled_at IS NULL;

CREATE TRIGGER emergency_credit_advances_set_updated_at
    BEFORE UPDATE ON emergency_credit_advances
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE emergency_credit_repayments (
    repayment_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    advance_id uuid NOT NULL REFERENCES emergency_credit_advances(advance_id),
    payment_id uuid NOT NULL REFERENCES payments(payment_id),
    repaid_kwh numeric(18, 6) NOT NULL,
    repaid_minor_units bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT emergency_credit_repayments_repaid_kwh_check CHECK (repaid_kwh > 0),
    CONSTRAINT emergency_credit_repayments_repaid_money_check CHECK (repaid_minor_units >= 0),
    CONSTRAINT emergency_credit_repayments_advance_payment_unique UNIQUE (advance_id, payment_id)
);

CREATE INDEX emergency_credit_repayments_payment_id_idx
    ON emergency_credit_repayments (payment_id);

CREATE TABLE meter_commands (
    command_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    meter_id text NOT NULL REFERENCES smart_meters(meter_id),
    command_type text NOT NULL,
    status text NOT NULL DEFAULT 'requested',
    requested_by text NOT NULL,
    reason text NOT NULL,
    requested_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    acknowledged_at timestamptz,
    failure_reason text,
    CONSTRAINT meter_commands_command_type_check CHECK (
        command_type IN ('credit_meter', 'disconnect_meter', 'reconnect_meter', 'request_meter_status')
    ),
    CONSTRAINT meter_commands_status_check CHECK (
        status IN ('requested', 'queued', 'sent', 'acknowledged', 'failed', 'expired')
    ),
    CONSTRAINT meter_commands_requested_by_check CHECK (requested_by <> ''),
    CONSTRAINT meter_commands_reason_check CHECK (reason <> ''),
    CONSTRAINT meter_commands_sent_at_check CHECK (
        status NOT IN ('sent', 'acknowledged') OR sent_at IS NOT NULL
    ),
    CONSTRAINT meter_commands_acknowledged_at_check CHECK (
        (status = 'acknowledged') = (acknowledged_at IS NOT NULL)
    ),
    CONSTRAINT meter_commands_acknowledged_order_check CHECK (
        acknowledged_at IS NULL OR acknowledged_at >= sent_at
    ),
    CONSTRAINT meter_commands_failure_reason_check CHECK (
        (status = 'failed') = (failure_reason IS NOT NULL)
        AND (failure_reason IS NULL OR failure_reason <> '')
    )
);

CREATE INDEX meter_commands_meter_id_requested_at_idx
    ON meter_commands (meter_id, requested_at DESC);

CREATE INDEX meter_commands_pending_requested_at_idx
    ON meter_commands (requested_at, command_id)
    WHERE status IN ('requested', 'queued', 'sent');

CREATE TABLE audit_events (
    audit_event_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id text NOT NULL REFERENCES sites(site_id),
    actor_id text NOT NULL,
    action text NOT NULL,
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT audit_events_actor_id_check CHECK (actor_id <> ''),
    CONSTRAINT audit_events_action_check CHECK (action <> ''),
    CONSTRAINT audit_events_subject_type_check CHECK (subject_type <> ''),
    CONSTRAINT audit_events_subject_id_check CHECK (subject_id <> '')
);

CREATE INDEX audit_events_subject_idx
    ON audit_events (site_id, subject_type, subject_id, occurred_at DESC);

CREATE INDEX audit_events_actor_id_occurred_at_idx
    ON audit_events (site_id, actor_id, occurred_at DESC);
