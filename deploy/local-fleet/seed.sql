-- Local demo metadata comes from the same site fixture as the frontend mock.
CREATE TEMP TABLE demo_sites AS
SELECT * FROM jsonb_to_recordset(:'sites_json'::jsonb) AS s(
    site_id text, name text, country text, region text, operator_id text,
    lat double precision, lng double precision, status text
);

INSERT INTO grid_operators(operator_id, name)
SELECT DISTINCT operator_id, 'Demo ' || operator_id FROM demo_sites
WHERE operator_id IS NOT NULL
ON CONFLICT (operator_id) DO NOTHING;

INSERT INTO sites(site_id, name, country, region, operator_id, lat, lng, status)
SELECT site_id, name, country, region, operator_id, lat, lng, status FROM demo_sites
ON CONFLICT (site_id) DO UPDATE SET
    name=EXCLUDED.name, country=EXCLUDED.country, region=EXCLUDED.region,
    operator_id=EXCLUDED.operator_id, lat=EXCLUDED.lat, lng=EXCLUDED.lng, status=EXCLUDED.status;
