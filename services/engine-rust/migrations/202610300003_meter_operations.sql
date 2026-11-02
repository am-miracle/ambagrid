-- A funded entitlement can have many historical delivery attempts, but only
-- one current attempt. Original commands and their published outcomes survive.
ALTER TABLE meter_commands ADD COLUMN replaces_command_id uuid UNIQUE REFERENCES meter_commands(command_id),
 ADD CONSTRAINT meter_commands_replacement_credit_check CHECK (replaces_command_id IS NULL OR credit_id IS NOT NULL);
DROP INDEX meter_commands_credit_id_idx;
CREATE UNIQUE INDEX meter_commands_credit_root_idx ON meter_commands(credit_id)
    WHERE credit_id IS NOT NULL AND replaces_command_id IS NULL;

CREATE TABLE command_reconciliation_cases (
    command_id uuid PRIMARY KEY REFERENCES meter_commands(command_id),
    state text NOT NULL CHECK (state IN ('open','investigating','resolved')),
    owner text NOT NULL,
    needs_review boolean NOT NULL DEFAULT false,
    finding text NOT NULL CHECK (finding IN ('unknown','delivered','not_delivered')),
    disposition text NOT NULL CHECK (disposition IN ('investigate','delivered','redeliver','refund')),
    reason text NOT NULL CHECK (length(trim(reason)) > 0),
    evidence text NOT NULL CHECK (length(trim(evidence)) > 0),
    resolved_by text,
    replacement_command_id uuid UNIQUE REFERENCES meter_commands(command_id),
    refund_reference text UNIQUE,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((state = 'resolved') = (resolved_by IS NOT NULL)),
    CHECK ((disposition = 'redeliver') = (replacement_command_id IS NOT NULL)),
    CHECK ((disposition = 'refund') = (refund_reference IS NOT NULL))
);
CREATE INDEX command_reconciliation_cases_state_idx ON command_reconciliation_cases(state, updated_at);
CREATE TABLE meter_credit_refunds (
    credit_id uuid PRIMARY KEY REFERENCES energy_credits(credit_id),
    command_id uuid NOT NULL UNIQUE REFERENCES meter_commands(command_id),
    reference text NOT NULL UNIQUE,
    actor text NOT NULL,
    evidence text NOT NULL,
    kwh numeric(18,6) NOT NULL CHECK (kwh > 0),
    money_minor bigint NOT NULL CHECK (money_minor >= 0),
    refunded_at timestamptz NOT NULL DEFAULT now()
);
-- Capability profiles are commissioned per meter; absent profiles deny manual
-- command creation. Edge adapters remain the final hardware capability check.
CREATE TABLE meter_command_profiles (
    meter_id text PRIMARY KEY REFERENCES smart_meters(meter_id),
    relay_control boolean NOT NULL DEFAULT false,
    status_read boolean NOT NULL DEFAULT false,
    prepaid_credit boolean NOT NULL DEFAULT false
);
CREATE TABLE meter_command_observations (
    command_id uuid PRIMARY KEY REFERENCES meter_commands(command_id),
    observed_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    relay_closed boolean,
    balance_kwh double precision,
    tamper_detected boolean,
    firmware_version text,
    fault_code text
);

-- Keep the attempt chain valid even if another command writer is introduced.
CREATE FUNCTION guard_credit_delivery_attempt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE prior meter_commands;
BEGIN
    IF NEW.credit_id IS NULL THEN RETURN NEW; END IF;
    PERFORM 1 FROM energy_credits WHERE credit_id=NEW.credit_id FOR UPDATE;
    IF EXISTS(SELECT 1 FROM meter_credit_refunds WHERE credit_id=NEW.credit_id) THEN
        RAISE EXCEPTION 'refunded credit cannot be delivered';
    END IF;
    IF NEW.replaces_command_id IS NOT NULL THEN
        SELECT * INTO prior FROM meter_commands WHERE command_id=NEW.replaces_command_id FOR UPDATE;
        IF NOT FOUND OR prior.credit_id IS DISTINCT FROM NEW.credit_id
            OR prior.meter_id<>NEW.meter_id OR prior.credit_kwh IS DISTINCT FROM NEW.credit_kwh
            OR prior.command_type<>'credit_meter' OR NEW.command_type<>'credit_meter'
            OR prior.status NOT IN ('failed','expired') THEN
            RAISE EXCEPTION 'invalid credit delivery attempt';
        END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER meter_commands_credit_attempt_guard BEFORE INSERT ON meter_commands
    FOR EACH ROW EXECUTE FUNCTION guard_credit_delivery_attempt();
