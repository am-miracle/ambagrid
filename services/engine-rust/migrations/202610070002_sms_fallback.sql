ALTER TABLE alerts DROP CONSTRAINT alerts_asset_id_fkey;

ALTER TABLE alerts
    ADD COLUMN asset_ref_id text GENERATED ALWAYS AS (
        CASE WHEN kind = 'site_outage' AND asset_id = 'site' THEN NULL ELSE asset_id END
    ) STORED,
    ADD CONSTRAINT alerts_asset_ref_id_fkey FOREIGN KEY (asset_ref_id) REFERENCES assets(asset_id),
    ADD CONSTRAINT alerts_site_scope_check CHECK (
        (kind = 'site_outage' AND asset_id = 'site')
        OR (kind <> 'site_outage' AND asset_id <> 'site')
    );

UPDATE alerts AS alert
SET kind = 'battery_overheat'
FROM assets AS asset
WHERE alert.asset_id = asset.asset_id
  AND asset.asset_type = 'battery_bms'
  AND alert.kind = 'internal_temperature';

DROP INDEX alerts_asset_id_kind_open_uidx;
CREATE UNIQUE INDEX alerts_site_asset_kind_open_uidx
    ON alerts (site_id, asset_id, kind)
    WHERE status = 'open';

CREATE UNIQUE INDEX alerts_source_event_id_uidx
    ON alerts (source_event_id)
    WHERE source_event_id LIKE 'edge:%';

CREATE TABLE sms_fallback_events (
    event_key text PRIMARY KEY,
    site_id text NOT NULL REFERENCES sites(site_id),
    sequence bigint NOT NULL CHECK (sequence > 0),
    asset_id text NOT NULL,
    code text NOT NULL CHECK (code IN ('BATTERY_OVERHEAT', 'INVERTER_FAILURE', 'TAMPER_DETECTED', 'SITE_OUTAGE')),
    event_at timestamptz NOT NULL,
    alert_id uuid NOT NULL REFERENCES alerts(alert_id),
    replay_received_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, sequence)
);

CREATE TABLE sms_fallback_receipts (
    provider_message_id text PRIMARY KEY,
    event_key text NOT NULL REFERENCES sms_fallback_events(event_key),
    gateway_id text NOT NULL,
    sender text NOT NULL,
    recipient text NOT NULL,
    received_at timestamptz NOT NULL,
    outbound_cost_minor bigint NOT NULL CHECK (outbound_cost_minor >= 0),
    inbound_cost_minor bigint NOT NULL CHECK (inbound_cost_minor >= 0),
    currency char(3) NOT NULL
);
