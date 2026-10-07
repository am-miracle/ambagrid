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
- `internal/adapter/simulator` simulates meter, battery, and inverter hardware.
- `internal/collector` normalizes adapter readings and persists them.
- `internal/domain` owns queue events, records, state, statistics, and errors.
- `internal/repository/sqlite` owns the durable adapter, with schema setup,
  queries, mutations, and capacity policy in separate files.
- `internal/uploader` drains the queue in sequence order and POSTs batches to
  the ingestion service; retries with exponential backoff on failure.
- `internal/health` owns the HTTP health endpoint and Prometheus collector.
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

`EDGE_MIN_FILESYSTEM_FREE_BYTES=0` is useful for development only. Deployed
gateways default to a 256 MiB filesystem reserve.

## Configuration

| Variable | Default | Meaning |
| --- | ---: | --- |
| `EDGE_SITE_ID` | required | Site written onto every queued event. |
| `EDGE_GATEWAY_ID` | required | Stable gateway identity used with the sequence as the upstream idempotency key. |
| `EDGE_QUEUE_PATH` | `/var/lib/ambagrid-edge/queue.db` | Local SQLite database path. |
| `EDGE_QUEUE_MAX_BYTES` | `536870912` | Total storage budget used to derive the database, WAL, and payload limits. |
| `EDGE_QUEUE_MAX_EVENT_BYTES` | `1048576` | Largest individual event; must not exceed 5% of the total budget. |
| `EDGE_QUEUE_WARNING_PERCENT` | `70` | Payload occupancy that changes health to `warning`. |
| `EDGE_QUEUE_CRITICAL_RESERVE_PERCENT` | `10` | Payload capacity held back from normal telemetry. |
| `EDGE_MIN_FILESYSTEM_FREE_BYTES` | `268435456` | Filesystem reserve below which all new writes stop. |
| `EDGE_SIMULATOR_ENABLED` | `false` | Run the simulated meter, battery, and inverter source. |
| `EDGE_COLLECTOR_INTERVAL` | `5s` | Interval between simulated collection cycles. |
| `EDGE_REGION` | `africa-west` | Region used in normalized MQTT topic envelopes. |
| `EDGE_HEALTH_ADDR` | `:2112` | Local health HTTP listen address. |
| `EDGE_UPLOADER_ENABLED` | `false` | Start the uploader that drains the queue to the ingestion service. |
| `EDGE_UPLOADER_ENDPOINT` | required | Full URL of the ingestion HTTP endpoint, e.g. `http://ingest:8090/v1/ingest`. |
| `EDGE_UPLOADER_API_KEY` | required | Bearer token the ingestion service uses to identify this site. |
| `EDGE_UPLOADER_BATCH_SIZE` | `50` | Max records per HTTP request. |
| `EDGE_UPLOADER_BATCH_MAX_BYTES` | `1048576` | Max total payload bytes per batch. |
| `EDGE_UPLOADER_POLL_INTERVAL` | `5s` | How often the uploader checks for ready records. |
| `EDGE_UPLOADER_TIMEOUT` | `30s` | HTTP request timeout per batch upload. |
| `EDGE_UPLOADER_BASE_DELAY` | `1s` | Initial backoff delay after a failed upload. |
| `EDGE_UPLOADER_MAX_DELAY` | `5m` | Ceiling on exponential backoff. |
| `EDGE_UPLOADER_MAX_RETRIES` | `20` | Max retry attempts before a record is abandoned. |

Each collection cycle writes one normalized record for a smart meter, battery
BMS, and solar inverter. Records carry their site ID, site-local sequence,
measurement timestamp, edge receive timestamp, device ID, asset type, payload,
and lifecycle status. The queue database is bound to one site identity, so its
SQLite `AUTOINCREMENT` sequence is monotonic for that site and is never reused.

The status lifecycle is:

```text
persisted -> pending_upload -> uploaded -> acknowledged -> expired
```

The collector first commits `persisted`, then promotes the record to
`pending_upload`. Startup recovery promotes any `persisted` record left by a
crash, preserving the reading without allowing it to become stranded.

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
3. POSTs batches to the ingestion service using `(site_id, sequence)` as the
   idempotency key — the server deduplicates on this pair.
4. Calls `Ack` for each sequence the server accepts, `MarkFailed` with
   exponential backoff for rejections or transport errors.

Every upload includes the current queue depth, oldest pending timestamp, and
most recent measurement timestamp. When the queue is empty, the uploader still
sends this envelope as a heartbeat on its normal poll interval. This lets the
server distinguish a connected gateway with delayed measurements from a site
that has stopped contacting the platform.

Delivery is intentionally at-least-once. A process crash after upstream
delivery and before `Ack` causes a safe duplicate rather than data loss.
