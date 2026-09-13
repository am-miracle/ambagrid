# api-go

Read-only HTTP API over the operational database: asset latest state, alerts,
and site rollups for the control room.

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
```

With no telemetry ingested yet, every listing is empty. Run the simulator and
the engine to give it something to serve:

```bash
go run scripts/virtual_meter.go
make engine-serve
```

## Layout

```text
main.go                        wiring: config -> pool -> services -> server
internal/config/               environment wiring
internal/domain/               read model: assets, alerts, sites
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
