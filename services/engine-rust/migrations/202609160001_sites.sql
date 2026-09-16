CREATE TABLE grid_operators (
    operator_id text PRIMARY KEY,
    name text NOT NULL,
    CONSTRAINT grid_operators_operator_id_check CHECK (operator_id <> ''),
    CONSTRAINT grid_operators_name_check CHECK (name <> '')
);

CREATE TABLE sites (
    site_id text PRIMARY KEY,
    name text NOT NULL,
    country text,
    region text,
    operator_id text REFERENCES grid_operators(operator_id),
    lat double precision,
    lng double precision,
    status text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sites_site_id_check CHECK (site_id <> ''),
    CONSTRAINT sites_name_check CHECK (name <> ''),
    CONSTRAINT sites_country_check CHECK (country IS NULL OR country <> ''),
    CONSTRAINT sites_region_check CHECK (region IS NULL OR region <> ''),
    CONSTRAINT sites_lat_check CHECK (lat IS NULL OR lat BETWEEN -90 AND 90),
    CONSTRAINT sites_lng_check CHECK (lng IS NULL OR lng BETWEEN -180 AND 180),
    CONSTRAINT sites_status_check CHECK (status <> '')
);

CREATE TRIGGER sites_set_updated_at
    BEFORE UPDATE ON sites
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

-- Preserve sites already represented by assets or historical alerts. Their
-- IDs are the only trustworthy metadata available during migration. Ownership
-- stays NULL until a GridOperator is assigned through a production provisioning
-- workflow.
INSERT INTO sites (site_id, name)
SELECT site_id, site_id
FROM (
    SELECT site_id FROM assets
    UNION
    SELECT site_id FROM alerts
) existing_sites;

ALTER TABLE assets
    ADD CONSTRAINT assets_site_fk
    FOREIGN KEY (site_id)
    REFERENCES sites(site_id);

ALTER TABLE alerts
    ADD CONSTRAINT alerts_site_fk
    FOREIGN KEY (site_id)
    REFERENCES sites(site_id);
