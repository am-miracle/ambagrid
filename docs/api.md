# Read API

`services/api-go` exposes what the platform already knows — asset latest state
and the alert lifecycle — over HTTP, so operators and the control room can see
it without `psql` or `rpk`.

It is deliberately read-only. The engine owns every write to the operational
database; the API only reads what the engine has already made durable. Actions
that change grid state (resolving an alert, issuing a meter command) belong in
the engine's action layer, not behind a convenience endpoint here.

## Shape of the Service

```text
HTTP request
  -> controller   routing, query parsing, JSON encoding, status codes
  -> services     page-size limits, identifier validation, what is read together
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
  internal/domain/               read model: assets, alerts, sites
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

Sites are derived from `assets.site_id`; there is no sites table yet.

```json
{
  "site_id": "site-01",
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
    "resolved_by": "operator-0101",
    "resolutions": [
      {
        "resolution_id": "1c4dff79-cb15-48e6-8a82-a93417fba80f",
        "resolved_at": "2026-09-02T02:58:31.546336+01:00",
        "resolution_note": "fan cleaned",
        "resolved_by": "operator-0101",
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
- **`GET /v1/sites`.** The only listing that aggregates rather than seeks: it
  scans every asset and every open alert regardless of page. Acceptable while a
  deployment is one operator's grid; the fix when it stops being acceptable is
  a continuous aggregate or a real sites table, not a bigger query.
- **Read replicas.** Every connection is opened `default_transaction_read_only`
  and the service issues no writes, so pointing `DATABASE_URL` at a replica
  works. Expect replication lag to show up as an alert appearing a moment after
  the engine opened it.

Every request carries a deadline into Postgres, and `DB_STATEMENT_TIMEOUT` is
the server-side backstop: without it a query whose client gave up keeps running
and pins the connection that cancelling it was meant to free.

## Not Here Yet

- **Authentication.** There is none. Run it behind a gateway that terminates
  TLS and authenticates callers; do not expose it to the internet as is.
- **Time-series readings.** `/v1/assets` serves latest state only. Hypertable
  reads (a meter's voltage over a day) need their own downsampling and
  windowing design rather than a `limit` on raw rows.
- **Customers, balances, payments, commands.** Those ontology objects have no
  tables yet. When they land, they are new `/v1` collections, not changes to
  these.
- **Writes.** Resolving an alert stays an engine action.
