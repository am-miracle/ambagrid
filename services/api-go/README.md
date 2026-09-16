# api-go

HTTP API over the operational database: asset latest state, alerts, site
rollups, and narrow operator commands for the control room.

Full endpoint reference and design notes: [docs/api.md](../../docs/api.md).

## Run It Locally

The database must exist and the engine's migrations must be applied:

```bash
docker compose up -d database
make db-migrate
```

Then, from the repository root:

```bash
make api-serve
```

Or directly:

```bash
DATABASE_URL="postgres://ambagrid_admin:ambagrid_secure_pass@localhost:5432/ambagrid_operational" \
  LOG_FORMAT=text go run .
```

```bash
curl http://localhost:8081/v1/sites
curl "http://localhost:8081/v1/alerts?status=open&severity=critical"
curl -X POST http://localhost:8081/v1/alerts/0bb99171-6d9a-42d4-8124-a5d995b10fd4/resolve \
  -H "Content-Type: application/json" \
  -H "X-Actor-Id: actor-0101" \
  -d '{"resolution_note":"fan cleaned"}'
```

With no telemetry ingested yet, every listing is empty. Run the simulator and
the engine to give it something to serve:

```bash
docker compose exec database psql -U ambagrid_admin -d ambagrid_operational \
  -c "INSERT INTO grid_operators (operator_id, name) VALUES ('operator-0101', 'Demo Operator') ON CONFLICT (operator_id) DO NOTHING; INSERT INTO sites (site_id, name, country, region, operator_id) VALUES ('ng-kaji-01', 'Kajiado 1', 'KE', 'Kajiado', 'operator-0101') ON CONFLICT (site_id) DO NOTHING"
go run scripts/virtual_meter.go -sites 1
make engine-serve
make outbox-publish
```

This direct SQL path is limited to local development and initial bootstrap.
Production provisioning must go through the authenticated provisioning
workflow when it is available. Provision every site ID before its first
telemetry arrives so a typo cannot create a ghost site.

## Layout

```text
main.go                        wiring: config -> pool -> services -> server
internal/config/               environment wiring
internal/domain/               assets, alerts, sites, command inputs
internal/page/                 keyset pagination and opaque cursors
internal/controller/           HTTP: routes, middleware, DTOs, error mapping
internal/services/             application layer and repository ports
internal/repository/postgres/  SQL, connection pool
```

Dependencies point inward. A controller may use the services; a service may use
its repository ports; neither the domain nor the services import `net/http` or
`pgx`. New endpoints follow the same path: a domain type if the object is new,
a repository query, a service method, then a handler and its DTO.

## Tests

```bash
gofmt -w .
go test -race ./...
```

The handler tests build the whole HTTP stack — routing, middleware, encoding,
status mapping — over fake services, so they need no database. The repository
package is the layer that does; it is covered by exercising the service against
a local Postgres with the engine's migrations applied.
