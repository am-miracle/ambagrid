# AmbaGrid

AmbaGrid is an open-source digital brain for solar mini-grids.

Across Africa, many communities get power from small local solar stations instead of one large national grid. These stations are far apart, hard to monitor, and expensive to manage. When a battery overheats, an inverter fails, or demand spikes in a village, operators may not know until the lights go out.

AmbaGrid gives operators a live digital twin of their grid: solar panels, batteries, inverters, smart meters, households, and sites. It listens to field telemetry in real time, turns that data into a durable stream, and gives software agents and human operators enough context to prevent outages before they become blackouts.

## Start Locally

You need Docker Desktop and Go installed.

```bash
docker compose up -d --build
go run scripts/virtual_meter.go
```

Open Redpanda Console:

```text
http://localhost:8080
```

You should see simulator records arrive in:

```text
telemetry.ingested
```

For a short smoke test:

```bash
go run scripts/virtual_meter.go -sites 1 -meters-per-site 2 -interval 1s -duration 5s -log-publishes
```

Serve the operator API (asset state, alerts, and alert resolution over HTTP):

```bash
make api-serve
```

```bash
curl http://localhost:8081/v1/sites
```

Apply database migrations explicitly:

```bash
make db-migrate
```

For local development or bootstrap only, provision the simulator's first
GridOperator and site directly in SQL before accepting telemetry:

```bash
docker compose exec database psql -U ambagrid_admin -d ambagrid_operational \
  -c "INSERT INTO grid_operators (operator_id, name) VALUES ('operator-0101', 'Demo Operator') ON CONFLICT (operator_id) DO NOTHING; INSERT INTO sites (site_id, name, country, region, operator_id) VALUES ('ng-kaji-01', 'Kajiado 1', 'KE', 'Kajiado', 'operator-0101') ON CONFLICT (site_id) DO NOTHING"
```

Repeat that insert for each site ID you intend to simulate. Unknown site IDs
are rejected by the asset foreign key instead of silently becoming sites.
Production provisioning must use the authenticated provisioning workflow when
it is available; direct SQL is not a production interface.

Start the Rust engine without applying migrations:

```bash
make engine-serve
```

Drain durable alert events from Postgres to Redpanda:

```bash
make outbox-publish
```

## Architecture

```text
Solar assets
   -> MQTT
   -> Go ingestion bridge
   -> Redpanda telemetry stream
   -> Rust engine + TimescaleDB
   -> Go API
   -> React control room
```

Read the full system design in [docs/architecture.md](docs/architecture.md).

## Local Service Addresses

From your host machine:

```text
MQTT:              localhost:1883
MQTT WebSockets:   localhost:9001
Redpanda Kafka:    localhost:9092
Redpanda Console:  localhost:8080
PostgreSQL:        localhost:5432
Operator API:      localhost:8081
```

From inside Docker Compose:

```text
MQTT:      mqtt-broker:1883
Redpanda:  redpanda:29092
Postgres:  database:5432
```

## Project Docs

- [Architecture](docs/architecture.md)
- [Operator API](docs/api.md)
- [Hardware integration](docs/hardware-integration.md)
- [Ontology](docs/ontology.md)
- [Roadmap](docs/roadmap.md)
- [Revenue protection](docs/revenue-protection.md)
- [Edge reliability](docs/edge-reliability.md)
- [Hardware adapters](docs/hardware-adapters.md)
- [Investor reporting](docs/investor-reporting.md)
- [Contributing](CONTRIBUTING.md)
- [License](LICENSE)

## License

AmbaGrid is licensed under the Apache License 2.0.
