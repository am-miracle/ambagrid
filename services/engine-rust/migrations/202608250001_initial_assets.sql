CREATE EXTENSION IF NOT EXISTS timescaledb;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE FUNCTION set_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

CREATE TABLE assets (
    asset_id text PRIMARY KEY,
    site_id text NOT NULL,
    asset_type text NOT NULL,
    internal_temperature real,
    last_seen_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT assets_asset_type_check
        CHECK (asset_type IN ('smart_meter', 'battery_bms', 'solar_inverter')),
    CONSTRAINT assets_asset_id_asset_type_unique
        UNIQUE (asset_id, asset_type)
);

CREATE INDEX assets_site_id_asset_type_idx ON assets (site_id, asset_type);
CREATE INDEX assets_site_id_last_seen_at_idx ON assets (site_id, last_seen_at);

CREATE TRIGGER assets_set_updated_at
    BEFORE UPDATE ON assets
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE smart_meter_state (
    asset_id text PRIMARY KEY,
    asset_type text NOT NULL DEFAULT 'smart_meter',
    reported_household_id text,
    relay_closed boolean,
    voltage real,
    current real,
    active_power real,
    frequency real,
    total_kwh double precision,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT smart_meter_state_asset_type_check
        CHECK (asset_type = 'smart_meter'),
    CONSTRAINT smart_meter_state_voltage_check
        CHECK (voltage IS NULL OR voltage >= 0),
    CONSTRAINT smart_meter_state_current_check
        CHECK (current IS NULL OR current >= 0),
    CONSTRAINT smart_meter_state_frequency_check
        CHECK (frequency IS NULL OR frequency >= 0),
    CONSTRAINT smart_meter_state_total_kwh_check
        CHECK (total_kwh IS NULL OR total_kwh >= 0),
    CONSTRAINT smart_meter_state_asset_fk
        FOREIGN KEY (asset_id, asset_type)
        REFERENCES assets(asset_id, asset_type)
        ON DELETE CASCADE
);

CREATE TRIGGER smart_meter_state_set_updated_at
    BEFORE UPDATE ON smart_meter_state
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE smart_meter_readings (
    time timestamptz NOT NULL,
    asset_id text NOT NULL,
    asset_type text NOT NULL DEFAULT 'smart_meter',
    internal_temperature real,
    reported_household_id text,
    relay_closed boolean,
    voltage real,
    current real,
    active_power real,
    frequency real,
    total_kwh double precision,
    CONSTRAINT smart_meter_readings_asset_type_check
        CHECK (asset_type = 'smart_meter'),
    CONSTRAINT smart_meter_readings_voltage_check
        CHECK (voltage IS NULL OR voltage >= 0),
    CONSTRAINT smart_meter_readings_current_check
        CHECK (current IS NULL OR current >= 0),
    CONSTRAINT smart_meter_readings_frequency_check
        CHECK (frequency IS NULL OR frequency >= 0),
    CONSTRAINT smart_meter_readings_total_kwh_check
        CHECK (total_kwh IS NULL OR total_kwh >= 0),
    CONSTRAINT smart_meter_readings_asset_id_check
        CHECK (asset_id <> '')
);

SELECT create_hypertable(
    'smart_meter_readings',
    by_range('time', INTERVAL '7 days'),
    if_not_exists => TRUE
);

CREATE INDEX smart_meter_readings_asset_id_time_idx
    ON smart_meter_readings (asset_id, time DESC);

CREATE TABLE battery_bms_state (
    asset_id text PRIMARY KEY,
    asset_type text NOT NULL DEFAULT 'battery_bms',
    battery_soc_pct real,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT battery_bms_state_asset_type_check
        CHECK (asset_type = 'battery_bms'),
    CONSTRAINT battery_bms_state_soc_check
        CHECK (battery_soc_pct IS NULL OR battery_soc_pct BETWEEN 0 AND 100),
    CONSTRAINT battery_bms_state_asset_fk
        FOREIGN KEY (asset_id, asset_type)
        REFERENCES assets(asset_id, asset_type)
        ON DELETE CASCADE
);

CREATE TRIGGER battery_bms_state_set_updated_at
    BEFORE UPDATE ON battery_bms_state
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE battery_bms_readings (
    time timestamptz NOT NULL,
    asset_id text NOT NULL,
    asset_type text NOT NULL DEFAULT 'battery_bms',
    internal_temperature real,
    battery_soc_pct real,
    CONSTRAINT battery_bms_readings_asset_type_check
        CHECK (asset_type = 'battery_bms'),
    CONSTRAINT battery_bms_readings_soc_check
        CHECK (battery_soc_pct IS NULL OR battery_soc_pct BETWEEN 0 AND 100),
    CONSTRAINT battery_bms_readings_asset_id_check
        CHECK (asset_id <> '')
);

SELECT create_hypertable(
    'battery_bms_readings',
    by_range('time', INTERVAL '7 days'),
    if_not_exists => TRUE
);

CREATE INDEX battery_bms_readings_asset_id_time_idx
    ON battery_bms_readings (asset_id, time DESC);

CREATE TABLE solar_inverter_state (
    asset_id text PRIMARY KEY,
    asset_type text NOT NULL DEFAULT 'solar_inverter',
    solar_irradiance real,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT solar_inverter_state_asset_type_check
        CHECK (asset_type = 'solar_inverter'),
    CONSTRAINT solar_inverter_state_irradiance_check
        CHECK (solar_irradiance IS NULL OR solar_irradiance >= 0),
    CONSTRAINT solar_inverter_state_asset_fk
        FOREIGN KEY (asset_id, asset_type)
        REFERENCES assets(asset_id, asset_type)
        ON DELETE CASCADE
);

CREATE TRIGGER solar_inverter_state_set_updated_at
    BEFORE UPDATE ON solar_inverter_state
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE solar_inverter_readings (
    time timestamptz NOT NULL,
    asset_id text NOT NULL,
    asset_type text NOT NULL DEFAULT 'solar_inverter',
    internal_temperature real,
    solar_irradiance real,
    CONSTRAINT solar_inverter_readings_asset_type_check
        CHECK (asset_type = 'solar_inverter'),
    CONSTRAINT solar_inverter_readings_irradiance_check
        CHECK (solar_irradiance IS NULL OR solar_irradiance >= 0),
    CONSTRAINT solar_inverter_readings_asset_id_check
        CHECK (asset_id <> '')
);

SELECT create_hypertable(
    'solar_inverter_readings',
    by_range('time', INTERVAL '7 days'),
    if_not_exists => TRUE
);

CREATE INDEX solar_inverter_readings_asset_id_time_idx
    ON solar_inverter_readings (asset_id, time DESC);

CREATE VIEW asset_readings AS
    SELECT
        time,
        asset_id,
        asset_type,
        internal_temperature
    FROM smart_meter_readings
    UNION ALL
    SELECT
        time,
        asset_id,
        asset_type,
        internal_temperature
    FROM battery_bms_readings
    UNION ALL
    SELECT
        time,
        asset_id,
        asset_type,
        internal_temperature
    FROM solar_inverter_readings;

CREATE TABLE alerts (
    alert_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    asset_id text NOT NULL REFERENCES assets(asset_id),
    site_id text NOT NULL,
    severity text NOT NULL,
    status text NOT NULL,
    reason text NOT NULL,
    opened_at timestamptz NOT NULL,
    resolved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT alerts_severity_check
        CHECK (severity IN ('info', 'warning', 'critical')),
    CONSTRAINT alerts_status_check
        CHECK (status IN ('open', 'resolved')),
    CONSTRAINT alerts_resolved_at_check
        CHECK (
            (status = 'open' AND resolved_at IS NULL)
            OR (status = 'resolved' AND resolved_at IS NOT NULL)
        )
);

CREATE INDEX alerts_asset_id_opened_at_idx
    ON alerts (asset_id, opened_at DESC);

CREATE INDEX alerts_site_id_status_opened_at_idx
    ON alerts (site_id, status, opened_at DESC);

CREATE INDEX alerts_open_idx
    ON alerts (site_id, severity, opened_at DESC)
    WHERE status = 'open';

CREATE TRIGGER alerts_set_updated_at
    BEFORE UPDATE ON alerts
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
