# Local fleet with the real backend

This optional Compose overlay populates the real local database and streams
generated device readings through HTTP ingestion, Redpanda, and the engine.
Keep `VITE_USE_MOCKS=false` in `apps/.env`.

The map uses site metadata from `apps/src/mocks/fixtures/sites.json`: 20 active
sites plus Ajaokuta, which remains commissioning without coordinates.

Lagos Epe runs `edge-agent-go` with one meter, battery and inverter for outage
tests. Its `outage_edge_queue` volume and `local-outage-gateway-01` identity
preserve event sequences across restarts. `fleet-simulator` runs the other 19
sites, each with ten meters, a battery, an inverter and a durable SQLite queue.
Simulated readings refresh every five seconds; upload heartbeats drive site health.

## Start

Run from the repository root. Build the two Go images if they are not available:

```sh
docker compose -f docker-compose.yml -f deploy/local-fleet/compose.yml build fleet-simulator edge-agent-go
```

The API and engine images must also be current; use the base Compose build when
network access to dependency registries is available. Start the prepared images:

```sh
docker compose -f docker-compose.yml -f deploy/local-fleet/compose.yml up -d --no-build api-go engine-rust outbox-publisher edge-agent-go fleet-simulator
```

The overlay creates `ambagrid_local_fleet`, applies migrations, and loads its
metadata. It preserves the default database and other test databases. Only
the local fleet database's demo site metadata is updated on subsequent seeds.
Do not use this overlay against a production database.

Run the frontend separately:

```sh
cd apps
npm run dev
```

Open http://localhost:3000 and allow up to 30 seconds for site-rollup polling.
The API is http://localhost:8081. The commissioning site has never reported;
the other 20 sites should show live connectivity.

Lagos Epe has `EDGE_SIMULATOR_FAULTS=true` in the base configuration, so it
continues to show critical equipment alerts while its connectivity is live.
The additional 19 sites generate healthy readings.

## Outage and recovery

Stop the Lagos Epe edge to observe loss of contact without affecting other sites:

```sh
docker compose -f docker-compose.yml -f deploy/local-fleet/compose.yml stop edge-agent-go
```

After the default two-minute offline threshold and the next site-rollup poll,
Epe's card shows offline health and retains its last readings. Restart it:

```sh
docker compose -f docker-compose.yml -f deploy/local-fleet/compose.yml up -d --no-build --no-deps edge-agent-go
```

Fresh heartbeats and telemetry restore live health. To test backlog accumulation,
disconnect only that container's Docker network instead of stopping its process;
collection will continue while upload is unavailable.

Stopping `fleet-simulator` makes its 19 sites lose contact together. Each site's
acknowledged queue history is retained for one hour and purged periodically;
unacknowledged readings remain queued for retry.
