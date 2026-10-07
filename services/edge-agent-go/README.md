# AmbaGrid Edge Agent

The edge agent owns the site-local durable queue used during weak or absent
connectivity. Milestone 3 provides SQLite persistence, site-local monotonic
sequence numbers, simulated hardware collection, capacity admission rules, and
local health output. It does not upload events yet.

## Structure

The service follows the same domain-and-adapter boundaries as the other AmbaGrid
services:

- `internal/config` owns environment configuration.
- `internal/adapter/simulator` simulates meter, battery, and inverter hardware.
- `internal/collector` normalizes adapter readings and persists them.
- `internal/domain` owns queue events, records, state, statistics, and errors.
- `internal/repository/sqlite` owns the durable adapter, with schema setup,
  queries, mutations, and capacity policy in separate files.
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

The uploader added in the next milestone should:

1. Read `Ready` records in sequence order.
2. Publish using `(site_id, sequence)` as its idempotency key.
3. Call `MarkUploaded` after publishing, then `Ack` after upstream acknowledgement.
4. Call `MarkFailed` with a future retry time after a failed attempt.

Delivery is intentionally at least once. A process crash after upstream
delivery and before `Ack` causes a safe duplicate rather than data loss.
