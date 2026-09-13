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

Serve the read API (asset state and alerts over HTTP):

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

Start the Rust engine without applying migrations:

```bash
make engine-serve
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
Read API:          localhost:8081
```

From inside Docker Compose:

```text
MQTT:      mqtt-broker:1883
Redpanda:  redpanda:29092
Postgres:  database:5432
```

## Project Docs

- [Architecture](docs/architecture.md)
- [Read API](docs/api.md)
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
