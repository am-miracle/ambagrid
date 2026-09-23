# Operator API

`services/api-go` exposes what the platform already knows — asset latest state
and the alert lifecycle — over HTTP, so operators and the control room can see
and act on it without `psql` or `rpk`.

Most endpoints are read-model queries. Operator commands are intentionally
narrow and audit-oriented: they validate the request, use the trusted Actor
identity supplied by the deployment gateway, then make the operational state
change durable. State-changing commands also write a business event to the
command outbox in the same transaction.

## Shape of the Service

```text
HTTP request
  -> controller   routing, query parsing, JSON encoding, status codes
  -> services     page-size limits, identifier validation, command validation
  -> repository   SQL against Postgres/TimescaleDB
```

Each layer only knows the one below it. The controller has no SQL and no
page-size policy; the services have no knowledge of HTTP. That is what lets the
handler tests exercise routing, middleware and encoding with fake services, and
no database.

```text
services/api-go/
  main.go                        wiring: config -> pool -> services -> server
  internal/config/               environment wiring
  internal/domain/               assets, alerts, sites, command inputs
  internal/page/                 keyset pagination and opaque cursors
  internal/controller/           HTTP: routes, middleware, DTOs, error mapping
  internal/services/             application layer and repository ports
  internal/repository/postgres/  SQL, connection pool
```

## Base URL and Versioning

```text
http://localhost:8081
```

Every object route is under `/v1`. Field deployments upgrade on their own
schedule, so a breaking change ships as `/v2` running alongside `/v1` rather
than as a redefinition of an existing route.

Additive changes are not breaking: clients must ignore unknown fields.

## Response Envelope

Collections:

```json
{
  "data": [{ "site_id": "site-01" }],
  "page": { "limit": 50, "next_cursor": "djEfc2l0ZS0wMQ" },
  "request_id": "e4d66d7f5bf091ef024fedbab53d14ea"
}
```

Single objects:

```json
{
  "data": { "asset_id": "met-0104" },
  "request_id": "e4d66d7f5bf091ef024fedbab53d14ea"
}
```

Errors:

```json
{
  "error": {
    "code": "invalid_argument",
    "message": "limit must not exceed 200",
    "request_id": "e4d66d7f5bf091ef024fedbab53d14ea"
  }
}
```

`request_id` is on every response, in the body and in the `X-Request-Id`
header, and it is the key every server-side log line for that request carries.
An inbound `X-Request-Id` is reused so a trace spans a gateway and this
service, but only if it is short and alphanumeric — an unfiltered header would
be an easy way to forge log lines.

Error codes: `invalid_argument` (400), `not_found` (404),
`method_not_allowed` (405), `deadline_exceeded` (504), `internal` (500).
Internal errors carry a generic message; the cause stays in the logs.

## Pagination

Listings are paginated by cursor, not by offset.

`OFFSET` makes Postgres re-walk and discard every skipped row, so paging deep
into a fleet's alert history gets linearly slower as that history grows. A
cursor carries the last row's sort key, which turns every page into the same
indexed range scan, and rows inserted while an operator pages cannot shift the
window and make a row appear twice or not at all.

```bash
curl "http://localhost:8081/v1/alerts?status=open&limit=50"
curl "http://localhost:8081/v1/alerts?status=open&limit=50&cursor=djEfMjAyNi0wOS0wMlQ..."
```

Cursors are opaque: they are this API's encoding of a sort key, and that key
may change. Follow `page.next_cursor`; do not construct one. An empty
`next_cursor` means the listing is exhausted.

`limit` defaults to 50 and is capped at 200. A request over the cap is a 400
rather than a silent clamp — a client that asked for 5,000 rows and quietly got
200 would page through a fleet believing it had seen everything.

Fleet-wide alert history is the exception, because no filter narrows its scan.
On `/v1/alerts` without `site_id` or `asset_id`, an omitted `status` defaults to
`open`; asking for anything else there both defaults and caps at 25 rows, so
`?status=resolved` returns 25 and `?status=resolved&limit=26` is a 400. Scope
the request with `site_id` or `asset_id` to page history at the normal size.
The response always reports the size actually applied in `page.limit`.

## Endpoints

### `GET /v1/sites`

Fleet rollup: asset count, most recent report, and open alerts by severity.

Sites are provisioned domain objects. A newly provisioned site is returned even
before its first asset reports; asset and alert fields are live rollups.
`operator_id` identifies the GridOperator organization responsible for the
site. It does not identify a human caller. Sites backfilled by the migration
may return `null` until ownership is assigned; production provisioning must
supply a GridOperator.

```json
{
  "site_id": "site-01",
  "name": "Kajiado 1",
  "country": "KE",
  "region": "Kajiado",
  "operator_id": "operator-0101",
  "lat": -1.8504,
  "lng": 36.7768,
  "status": "active",
  "asset_count": 3,
  "last_seen_at": "2026-09-03T01:58:31.535397+01:00",
  "open_alerts": { "total": 1, "critical": 1, "warning": 0, "info": 0 }
}
```

### `GET /v1/assets`

Latest known state of each asset, newest state per asset rather than history.

| Parameter | Values |
|---|---|
| `site_id` | any site ID |
| `asset_type` | `smart_meter`, `battery_bms`, `solar_inverter` |
| `limit`, `cursor` | see Pagination |

Each asset carries the shared header plus exactly one type-specific block:
`smart_meter`, `battery_bms` or `solar_inverter`. The block is absent — not
zeroed — for an asset that has been registered but has not yet reported its
type-specific metrics.

```json
{
  "asset_id": "met-0104",
  "site_id": "site-01",
  "asset_type": "smart_meter",
  "internal_temperature": 41.2,
  "last_seen_at": "2026-09-03T01:58:31.535397+01:00",
  "updated_at": "2026-09-03T01:58:31.535397+01:00",
  "smart_meter": {
    "reported_household_id": "house-0104",
    "relay_closed": true,
    "voltage": 231.4,
    "current": 3.2,
    "active_power": 740.5,
    "frequency": 50.01,
    "total_kwh": 812.25,
    "updated_at": "2026-09-03T01:58:31.542004+01:00"
  }
}
```

Assets are ordered by `asset_id`, which the primary key index serves under
every filter combination. Ordering a whole fleet by recency would need an index
that does not exist yet.

### `GET /v1/assets/{asset_id}`

The same object for one asset. 404 if it has never reported.

### `GET /v1/assets/{asset_id}/readings`

Historical chart data for one numeric metric. This endpoint returns aggregated
time buckets, never raw hypertable rows.

| Parameter | Required | Values |
|---|---|---|
| `from`, `to` | yes | RFC 3339 timestamps; `from` must be before `to` |
| `metric` | yes | depends on the asset type; see below |
| `interval` | yes | `1m`, `5m`, `15m`, `1h` |

Smart meters support `internal_temperature`, `voltage`, `current`,
`active_power`, `frequency`, and `total_kwh`. Battery BMS assets support
`internal_temperature` and `battery_soc_pct`. Solar inverters support
`internal_temperature` and `solar_irradiance`.

The largest permitted windows are 24 hours for `1m`, 7 days for `5m`, 30 days
for `15m`, and 366 days for `1h`. A metric that does not belong to the asset's
type, an invalid range, or a window over its limit returns 400. An unknown asset
returns 404; an asset with no readings returns an empty `points` array.

```json
{
  "data": {
    "asset_id": "met-0104",
    "asset_type": "smart_meter",
    "from": "2026-09-14T10:00:00Z",
    "to": "2026-09-14T11:00:00Z",
    "metric": "voltage",
    "interval": "5m",
    "aggregation": "avg",
    "points": [
      { "time": "2026-09-14T10:00:00Z", "value": 231.2 }
    ]
  },
  "request_id": "e4d66d7f5bf091ef024fedbab53d14ea"
}
```

Most metrics use an average for each bucket. `total_kwh` is a cumulative meter
counter, so it uses the maximum value in each bucket.

### `GET /v1/alerts`

Alerts newest first.

| Parameter | Values |
|---|---|
| `site_id`, `asset_id` | any ID |
| `status` | `open`, `resolved` |
| `severity` | `info`, `warning`, `critical` |
| `kind` | opaque string, e.g. `internal_temperature` |
| `limit`, `cursor` | see Pagination |

`kind` is matched as an opaque string because alert kinds grow with the
engine's policies; the API does not track a closed set it would fall behind on.
Unknown values for `status`, `severity` and `asset_type` are a 400, not an
empty result set that an operator would read as "nothing is wrong".

For fleet-wide requests, omitting `status` means `status=open`. Historical
fleet-wide pages such as `status=resolved` both default and cap at
`API_GLOBAL_HISTORY_PAGE_SIZE` rows unless scoped by `site_id` or `asset_id`;
see Pagination.

### `GET /v1/alerts/{alert_id}`

One alert with its resolution history inline:

```json
{
  "data": {
    "alert_id": "0bb99171-6d9a-42d4-8124-a5d995b10fd4",
    "asset_id": "met-0104",
    "site_id": "site-01",
    "kind": "internal_temperature",
    "severity": "critical",
    "status": "resolved",
    "reason": "meter internal temperature 71.0C above 70.0C",
    "opened_at": "2026-09-02T01:58:31.546336+01:00",
    "source_event_id": null,
    "resolved_at": "2026-09-02T02:58:31.546336+01:00",
    "resolution_note": "fan cleaned",
    "resolved_by": "actor-0101",
    "resolutions": [
      {
        "resolution_id": "1c4dff79-cb15-48e6-8a82-a93417fba80f",
        "resolved_at": "2026-09-02T02:58:31.546336+01:00",
        "resolution_note": "fan cleaned",
        "resolved_by": "actor-0101",
        "recorded_at": "2026-09-03T01:58:31.546336+01:00"
      }
    ]
  },
  "request_id": "e4d66d7f5bf091ef024fedbab53d14ea"
}
```

An alert can be resolved, reopened by the engine and resolved again, so the
history is a list. It is unpaginated: its length is bounded by how many times
an operator has resolved one alert, not by fleet size.

### `POST /v1/alerts/{alert_id}/resolve`

Resolves one open alert as an operator action.

The API does not authenticate users itself yet. Deploy it behind a gateway that
authenticates the caller and injects `X-Actor-Id`; the API treats that header
as trusted deployment metadata and records it as `resolved_by`. Do not expose
this endpoint directly to the internet.

Use this endpoint only when the operator is closing that alert instance. If the
asset is still violating the same policy, the next telemetry tick can open a
new alert for the same `asset_id` and `kind`.

```bash
curl -X POST "http://localhost:8081/v1/alerts/0bb99171-6d9a-42d4-8124-a5d995b10fd4/resolve" \
  -H "Content-Type: application/json" \
  -H "X-Actor-Id: actor-0101" \
  -d '{"resolution_note":"fan cleaned"}'
```

Request body:

```json
{
  "resolution_note": "fan cleaned"
}
```

Successful responses return the resolved alert in the normal object envelope.
Missing `X-Actor-Id` returns 401. A malformed alert ID, empty
`resolution_note`, empty Actor ID, or reserved `system` Actor ID returns
400. A missing alert returns 404. An alert that exists but is no longer open
returns 409.

### `GET /healthz` and `GET /readyz`

`/healthz` is liveness and never touches the database — restarting a replica
cannot fix a database outage, and a liveness probe that failed during one would
restart every replica at once. `/readyz` pings Postgres and returns 503 when it
is unreachable, so the replica leaves the load balancer instead.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | *(required)* | Postgres connection string |
| `API_HTTP_ADDR` | `:8081` | listen address |
| `API_REQUEST_TIMEOUT` | `10s` | per-request deadline, propagated to the query |
| `API_READ_HEADER_TIMEOUT` | `5s` | slow-header protection |
| `API_IDLE_TIMEOUT` | `60s` | keep-alive idle timeout |
| `API_SHUTDOWN_TIMEOUT` | `10s` | drain window on SIGTERM |
| `API_CORS_ALLOWED_ORIGINS` | `http://localhost:3000,http://localhost:5173` | control-room origins |
| `API_DEFAULT_PAGE_SIZE` | `50` | page size when `limit` is absent |
| `API_MAX_PAGE_SIZE` | `200` | page size ceiling |
| `API_GLOBAL_HISTORY_PAGE_SIZE` | `25` | default and ceiling for unscoped alert history |
| `DB_MAX_CONNS` | `10` | pool ceiling per replica |
| `DB_MIN_CONNS` | `1` | warm connections |
| `DB_QUERY_TIMEOUT` | `3s` | bounds waiting for a connection plus executing |
| `DB_STATEMENT_TIMEOUT` | `5s` | server-side backstop |
| `LOG_FORMAT` | JSON | set `text` for human-readable logs |

`DB_STATEMENT_TIMEOUT` must be at least `1ms`, and `DB_QUERY_TIMEOUT` must be
lower than `DB_STATEMENT_TIMEOUT`. The client-side timeout should fire first;
the statement timeout is the database-side cleanup backstop.

## Scaling Model

The service holds no state between requests, so scaling out is more replicas
behind a load balancer. What does not scale automatically:

- **Connections.** Each replica opens up to `DB_MAX_CONNS`. Multiply by replica
  count before raising it; past a few replicas the answer is a connection
  pooler, not a bigger pool.
- **Operator scope.** `/v1/sites` remains the fleet-wide read collection.
  Organization-scoped reads and future provisioning use
  `/v1/grid-operators/{grid_operator_id}/sites`, with the same prefix for other
  organization-owned collections. The path ID is a GridOperator ID; human
  identity continues to come from trusted Actor authentication metadata.
- **Read replicas.** The service now includes operator commands, so the primary
  `DATABASE_URL` must point at a writable database. Split read/write pools
  before pointing listings at replicas.
- **Outbox publisher.** State-changing operator commands and engine alert
  transitions write business events into `command_event_outbox`. Run
  `engine-rust publish-outbox` to lease unpublished rows, encode their internal
  JSON payloads as the topic's protobuf message, publish them to Redpanda, and
  mark them published. Delivery is at least once, so consumers should treat the
  outbox event ID as an idempotency key. The publisher backs off on broker
  failures and removes published rows after 30 days.

Every request carries a deadline into Postgres, and `DB_STATEMENT_TIMEOUT` is
the server-side backstop: without it a query whose client gave up keeps running
and pins the connection that cancelling it was meant to free.

## Not Here Yet

- **Built-in authentication.** There is none. Run it behind a gateway that
  terminates TLS, authenticates callers, and injects `X-Actor-Id` for
  operator commands; do not expose it to the internet as is.
- **Customer, balance, payment, and command APIs.** Those ontology objects have
  storage tables but no public handlers yet. When handlers land, they are new
  `/v1` collections, not changes to these.
- **General writes.** Alert resolution is the only operator command exposed
  here. Meter commands, payments, credits, and customer changes are not here yet.
