# AmbaGrid Edge Agent

The edge agent runs on-site near physical hardware. It collects readings from
meters, batteries, and inverters, persists them in a SQLite durable queue that
survives power loss and connectivity gaps, and uploads batches to the central
ingestion service over HTTP with exponential-backoff retry. Records are only
removed after the server acknowledges them.

## Structure

The service follows the same domain-and-adapter boundaries as the other AmbaGrid
services:

- `internal/config` owns environment configuration.
- `internal/adapter` defines the vendor-neutral telemetry and command contract,
  capability declaration, and typed errors every hardware adapter returns. Its
  `Ledger` gives any command adapter at-most-once delivery per command ID; see
  [the command ledger](../../docs/command-ledger.md).
- `internal/adapter/adaptertest` is the conformance suite each adapter runs via
  `adaptertest.RunTelemetry` and `adaptertest.RunCommand`, against fixtures in
  `internal/adapter/testdata/<adapter>/`.
- `internal/adapter/devicetest` gives test devices the stall, hold and recording
  controls the suite drives, without linking `testing` into the binary.
- `internal/adapter/mqtt` carries meter commands as MQTT v5 requests using the
  protocol and topic map in `docs/hardware-adapters.md`, over an autopaho
  (`github.com/eclipse/paho.golang`) connection in production.
- `internal/adapter/modbus` reads telemetry from any Modbus TCP device described
  by a YAML register map, through a pure decoder and a grid-x/modbus client.
- `internal/adapter/simulator` is the simulated meter adapter over simulated
  meter, battery, and inverter hardware. Its device keeps relay, balance and
  tamper state, and can take the meter offline to exercise stale last-seen
  times and command timeouts.
- `internal/commands` polls api-go for the site's meter commands, runs them in
  request order through the ledger, refuses expired and superseded relay
  commands, and delivers queued outcome reports.
- `internal/collector` validates, envelopes, and persists adapter readings without reshaping their payloads.
- `internal/domain` owns queue events, records, state, statistics, and errors.
- `internal/repository/sqlite` owns the durable adapter, with schema setup,
  queries, mutations, capacity policy, and the command ledger in separate files.
- `internal/uploader` drains the queue in sequence order and POSTs batches to
  the ingestion service; retries with exponential backoff on failure.
- `internal/health` owns the HTTP health endpoint and Prometheus collector.
- `internal/fallback` formats critical alerts and sends them through ModemManager.
- `main.go` only wires configuration, adapters, storage, and HTTP transport.

## Run

Set the stable site and gateway identity, then start the service:

```bash
EDGE_SITE_ID=ng-kaji-01 \
EDGE_GATEWAY_ID=gateway-01 \
EDGE_QUEUE_PATH=./data/queue.db \
EDGE_MIN_FILESYSTEM_FREE_BYTES=0 \
EDGE_SIMULATOR_ENABLED=true \
EDGE_UPLOADER_ENABLED=true \
EDGE_UPLOADER_ENDPOINT=http://localhost:8090/v1/ingest \
EDGE_UPLOADER_API_KEY=dev-key \
go run .
```

Local status is available at:

- `http://localhost:2112/health`
- `http://localhost:2112/metrics`

The repository Compose stack runs this path end to end with deterministic
critical simulator conditions:

```bash
docker compose up --build
```

It enables ingestion HTTP on port 8090, provisions the `lagos-epe` demo site,
persists the edge queue in the `edge_queue` volume, and exposes edge health on
port 2112. SMS remains disabled because the container has no physical modem;
the critical event identity and normal upload path still reach Kafka and the
engine.

`EDGE_MIN_FILESYSTEM_FREE_BYTES=0` is useful for development only. Deployed
gateways default to a 256 MiB filesystem reserve.

## Configuration

| Variable | Default | Meaning |
| --- | ---: | --- |
| `EDGE_SITE_ID` | required | Site written onto every queued event. |
| `EDGE_GATEWAY_ID` | required | Stable gateway identity carried with the site-local event sequence. |
| `EDGE_QUEUE_PATH` | `/var/lib/ambagrid-edge/queue.db` | Local SQLite database path. |
| `EDGE_QUEUE_MAX_BYTES` | `536870912` | Total storage budget used to derive the database, WAL, and payload limits. |
| `EDGE_QUEUE_MAX_EVENT_BYTES` | `1048576` | Largest individual event; must not exceed 5% of the total budget. |
| `EDGE_QUEUE_WARNING_PERCENT` | `70` | Payload occupancy that changes health to `warning`. |
| `EDGE_QUEUE_CRITICAL_RESERVE_PERCENT` | `10` | Payload capacity held back from normal telemetry. |
| `EDGE_MIN_FILESYSTEM_FREE_BYTES` | `268435456` | Filesystem reserve below which all new writes stop. |
| `EDGE_SIMULATOR_ENABLED` | `false` | Run the simulated meter, battery, and inverter source. |
| `EDGE_SIMULATOR_FAULTS` | `false` | Emit deterministic critical conditions for local end-to-end testing. |
| `EDGE_COLLECTOR_INTERVAL` | `5s` | Interval between simulated collection cycles. |
| `EDGE_REGION` | `africa-west` | Region used in normalized MQTT topic envelopes. |
| `EDGE_HEALTH_ADDR` | `:2112` | Local health HTTP listen address. |
| `EDGE_COMMAND_RETENTION` | `720h` | How long resolved meter commands stay in the ledger. Must outlast the longest upstream retry of one command ID. |
| `EDGE_COMMAND_PRUNE_INTERVAL` | `1h` | Interval between ledger pruning passes. Unresolved commands are never pruned. |
| `EDGE_COMMANDS_ENABLED` | `false` | Fetch meter commands from api-go and run them through the command ledger. |
| `EDGE_COMMAND_ADAPTER` | `simulator` | `simulator` (requires `EDGE_SIMULATOR_ENABLED`) or `mqtt`. |
| `EDGE_MQTT_BROKER` | required for `mqtt` | TLS broker URL, e.g. `ssl://broker:8883` or `mqtts://broker:8883`. |
| `EDGE_MQTT_CAPABILITIES_FILE` | required for `mqtt` | YAML capability declaration of the meters behind the broker. |
| `EDGE_MQTT_COMMAND_TOPIC` | `site/{site_id}/meter/{device_id}/cmd` | Command topic template; `{device_id}` must fill a whole level. |
| `EDGE_MQTT_REPLY_TOPIC` | `site/{site_id}/meter/{device_id}/cmd/reply` | Per-device reply topic template, sent as each request's `ResponseTopic`. |
| `EDGE_MQTT_RESPONSE_TIMEOUT` | `30s` | How long to wait for a device's reply; a PUBACK alone is not an answer. |
| `EDGE_MQTT_CLIENT_ID` | `ambagrid-edge-{gateway_id}` | Must be unique on the broker. |
| `EDGE_MQTT_USERNAME`, `EDGE_MQTT_PASSWORD` | empty | Required broker credentials unless insecure development is explicitly enabled. |
| `EDGE_MQTT_ALLOW_INSECURE` | `false` | Allow plaintext or anonymous command transport for isolated development. |
| `EDGE_MQTT_CA_FILE` | empty | Required certificate authority for TLS. |
| `EDGE_COMMANDS_ENDPOINT` | required | Base URL of api-go, e.g. `http://api:8081`. |
| `EDGE_COMMANDS_API_KEY` | `EDGE_UPLOADER_API_KEY` | Site key for api-go's `API_EDGE_API_KEYS`. |
| `EDGE_COMMANDS_POLL_INTERVAL` | `15s` | Interval between command polls and report deliveries. |
| `EDGE_COMMANDS_TIMEOUT` | `30s` | Bound on each api-go request and each command's device exchange. |
| `EDGE_UPLOADER_ENABLED` | `false` | Start the uploader that drains the queue to the ingestion service. |
| `EDGE_UPLOADER_ENDPOINT` | required | Full URL of the ingestion HTTP endpoint, e.g. `http://ingest:8090/v1/ingest`. |
| `EDGE_UPLOADER_API_KEY` | required | Bearer token the ingestion service uses to identify this site. |
| `EDGE_UPLOADER_BATCH_SIZE` | `50` | Max records per HTTP request. |
| `EDGE_UPLOADER_BATCH_MAX_BYTES` | `1048576` | Max total payload bytes per batch. |
| `EDGE_UPLOADER_POLL_INTERVAL` | `5s` | How often the uploader checks for ready records. |
| `EDGE_UPLOADER_TIMEOUT` | `30s` | HTTP request timeout per batch upload. |
| `EDGE_UPLOADER_BASE_DELAY` | `1s` | Initial backoff delay after a failed upload. |
| `EDGE_UPLOADER_MAX_DELAY` | `5m` | Ceiling on exponential backoff. |
| `EDGE_UPLOADER_MAX_RETRIES` | `20` | Max explicit server rejections before a record is abandoned; connectivity retries are unlimited. |
| `EDGE_SMS_ENABLED` | `false` | Enable one-way SMS fallback for critical alerts. |
| `EDGE_SMS_DESTINATION` | required when enabled | Platform phone number that receives fallback messages. |
| `EDGE_SMS_MODEM_ID` | `0` | ModemManager modem identifier passed to `mmcli`. |
| `EDGE_SMS_FAILURE_THRESHOLD` | `3` | Failed data uploads before a critical alert qualifies for SMS. |
| `EDGE_SMS_OFFLINE_AFTER` | `2m` | Time without a successful upload before SMS qualifies. |
| `EDGE_SMS_MAX_ATTEMPTS` | `3` | Maximum SMS attempts for one active incident. |
| `EDGE_SMS_HOURLY_LIMIT` | `6` | Site-wide attempt limit over one hour. |
| `EDGE_SMS_DAILY_LIMIT` | `20` | Site-wide attempt limit over 24 hours. |
| `EDGE_SMS_RETRY_INTERVAL` | `7m30s` | Minimum spacing between attempts, spreading three attempts over 15 minutes. |
| `EDGE_SMS_SEND_TIMEOUT` | `30s` | Maximum time allowed for one modem SMS create-and-send operation. |
| `EDGE_SMS_BATTERY_OVERHEAT_C` | `55` | Battery temperature that opens a critical fallback incident. |
| `EDGE_SMS_HEARTBEAT_ENABLED` | `true` | Send SMS liveness heartbeats while packet data is unavailable. |
| `EDGE_SMS_HEARTBEAT_INTERVAL` | `30m` | Spacing between heartbeats, sent in each heartbeat; whole seconds from `EDGE_SMS_OFFLINE_AFTER` (and at least 1m) up to 24h. |
| `EDGE_SMS_HEARTBEAT_DAILY_LIMIT` | `48` | Heartbeat attempts over 24 hours, counted separately from critical alerts. |
| `EDGE_SMS_DEBOUNCE` | `30s` | Time inverter failure, tamper, and site-outage signals must remain active before opening an incident. |

Each collection cycle writes one normalized record for a smart meter, battery
BMS, and solar inverter. Records carry their site ID, site-local sequence,
measurement timestamp, edge receive timestamp, device ID, asset type, payload,
and lifecycle status. The queue database is bound to one site identity, so its
SQLite `AUTOINCREMENT` sequence is monotonic for that site and is never reused.

The status lifecycle is:

```text
persisted -> pending_upload -> uploaded -> acknowledged -> expired
                     \-> abandoned (terminal server rejections only)
```

The collector first commits `persisted`, then promotes the record to
`pending_upload`. Startup recovery promotes any `persisted` record left by a
collector crash and returns any `uploaded` record interrupted before local
acknowledgement to `pending_upload`; the stable event identity makes that retry
idempotent upstream.

The queue admits payloads up to 65% of the total storage budget. SQLite's main
database is capped at 80% and its WAL is checkpointed around 5%. Before each
write, admission reserves enough room for both the WAL append and its checkpoint
copy into the database. The remaining space covers schema, indexes, shared
memory, and page overhead. The filesystem reserve applies the same projected
write size as a second guard for other processes sharing the disk.

Normal telemetry stops when it reaches the critical-reserve boundary. Critical
events can consume the remainder of the payload allowance. The queue never
silently evicts old events.

## Delivery contract

The uploader:

1. Reads `Ready` records in sequence order.
2. Stamps `uploaded_at` before sending so the server can distinguish event time,
   edge receive time, and upload time.
3. POSTs batches to the ingestion service with `(site_id, sequence)` as the
   stable event identity. Ingestion publishes every accepted delivery with the
   corresponding `source_event_id`; durable consumers deduplicate on that ID.
4. Atomically acknowledges each accepted sequence with its fallback-delivery
   bookkeeping, and calls `MarkFailed` with exponential backoff for rejections
   or transport errors. Connectivity failures retry indefinitely. Records that
   exhaust the server-rejection budget become `abandoned`, releasing later
   records for delivery while remaining visible in health metrics.

Every upload includes the current queue depth, oldest pending timestamp, and
most recent measurement timestamp. When the queue is empty, the uploader still
sends this envelope as a heartbeat on its normal poll interval. This lets the
server distinguish a connected gateway with delayed measurements from a site
that has stopped contacting the platform.

When packet data is down for `EDGE_SMS_OFFLINE_AFTER`, the SMS coordinator
sends `AMBAGRID|v=1|kind=HB|site=<site>|gw=<gateway>|seq=<n>|int=<seconds>|ts=<unix>`
every `EDGE_SMS_HEARTBEAT_INTERVAL`, after any due critical alerts. `int` carries
that interval so the platform derives the offline deadline from it. `seq` comes from a
dedicated durable counter, so heartbeats never consume event sequences. Each
attempt is persisted before sending, and attempts count only against
`EDGE_SMS_HEARTBEAT_DAILY_LIMIT`, never the critical-alert hourly or daily
limits. A failed send is retried after `EDGE_SMS_RETRY_INTERVAL`. Heartbeats
stop as soon as an HTTP upload succeeds.

Delivery is intentionally at-least-once. A lost response after upstream
delivery causes a safe retry rather than data loss. Ingestion is stateless, so
durable consumers deduplicate retried stream records by `source_event_id`.

Critical battery overheat, inverter failure, tamper, and site outage events are
stored as durable incidents. After three failed uploads or two minutes without
a successful upload, the edge agent sends a single-segment versioned SMS. A
successful normal replay removes the incident from the fallback candidate set.

Production command MQTT requires [TLS and broker topic ACLs](../../docs/mqtt-command-security.md).
Run `deploy/mqtt/test-acl.sh` from the repository root to exercise a disposable
TLS broker with the production policy. The anonymous development broker is not
suitable for hardware commands in production.

## Modbus telemetry

A Modbus TCP device can feed the collector without the simulator. Set
`EDGE_MODBUS_ADDRESS` to `host:port`, `EDGE_MODBUS_UNIT_ID` to its unit ID,
`EDGE_MODBUS_DEVICE_ID` to its platform asset ID, and `EDGE_MODBUS_MAP_FILE` to
its register map; `EDGE_MODBUS_TIMEOUT` (default `5s`) bounds each request. A
Victron Cerbo GX battery service uses `deploy/modbus/victron-battery.yaml`. The
shared `EDGE_COLLECTOR_INTERVAL` controls polling. See
[register maps](../../docs/hardware-adapters.md#modbus-register-maps).

## Integration tests

`go test -tags=integration ./...` also runs the MQTT adapter's conformance suite
over autopaho against an in-process MQTT v5 broker, and the Modbus client
against an in-process Modbus TCP server, so neither needs external services.
