# Hardware Integration

AmbaGrid should support many hardware brands without making the core platform vendor-specific.

The core rule is simple: adapters translate vendor payloads into the AmbaGrid telemetry model before data enters the main stream.

## Current MQTT Topic Shape

Smart meter telemetry enters through MQTT:

```text
africa-west/{site_id}/smartmeter/{device_id}/telemetry
```

Example:

```text
africa-west/ng-kaji-01/smartmeter/met-0101/telemetry
```

The Go ingestion bridge subscribes to:

```text
africa-west/+/smartmeter/+/telemetry
```

It writes raw payloads to Redpanda topic:

```text
telemetry.raw
```

## Payload Model

The simulator currently sends JSON shaped like `proto/telemetry.proto` so developers can inspect messages easily.

Core fields:

- `device_id`
- `device_type`
- `timestamp_utc`
- `site_id`
- `household_id`
- `metrics.voltage`
- `metrics.current`
- `metrics.active_power`
- `metrics.frequency`
- `metrics.total_kwh`
- `internal_temperature`
- `relay_closed`
- `battery_soc_pct`
- `solar_irradiance`

The platform should move toward Protobuf on the wire once the ingestion and engine contracts stabilize.

The simulator always emits `internal_temperature`, `relay_closed`,
`battery_soc_pct`, and `solar_irradiance` in JSON, even when their value is
zero or false. `household_id` is still omitted when empty.

## Sample MQTT → Kafka Payloads

The ingestion bridge validates each MQTT topic against `MQTT_TOPIC_FILTER`
before producing to Kafka: each of the filter's four meaningful segments
(region, site, device type, device) becomes an accepted value independently,
and a `+`/`#` wildcard segment means "accept any value" for that position.
The examples below show the same message under three filter configurations.

### Default filter

`MQTT_TOPIC_FILTER=africa-west/+/smartmeter/+/telemetry`

MQTT publish:

```text
topic:   africa-west/ng-kaji-01/smartmeter/met-0101/telemetry
payload: {"device_id":"met-0101","device_type":"DEVICE_TYPE_SMART_METER",
          "timestamp_utc":1745500000,"site_id":"ng-kaji-01",
          "household_id":"house-0101",
          "metrics":{"voltage":231.4,"current":6.7,"active_power":1.55,
                     "frequency":50.02,"total_kwh":1521.83},
          "internal_temperature":0,"relay_closed":false,
          "battery_soc_pct":0,"solar_irradiance":0}
```

Resulting Kafka record on `telemetry.raw`:

```text
Key:   met-0101
Value: <payload above, verbatim>
Headers:
  mqtt_topic  = africa-west/ng-kaji-01/smartmeter/met-0101/telemetry
  region      = africa-west
  site_id     = ng-kaji-01
  device_type = smartmeter
  device_id   = met-0101
  mqtt_qos = 1  mqtt_retained = false  mqtt_duplicate = false  mqtt_message_id = 0
```

### Non-default filter (widening which already-normalized topics are accepted)

`MQTT_TOPIC_FILTER=africa-east/+/inverter/+/telemetry`

MQTT publish:

```text
topic:   africa-east/lg-abuja-03/inverter/inv-0007/telemetry
payload: <AmbaGrid telemetry JSON, shaped like the Payload Model above>
```

Result: accepted and produced, with headers `region=africa-east`,
`device_type=inverter`, `site_id=lg-abuja-03`, `device_id=inv-0007`. The
bridge derives its accepted region/device-type from whatever filter it was
started with, so a config change is enough to widen *which topics* it will
ingest from.

This is **not** the same as onboarding a new vendor's hardware. The bridge
only validates topic structure — it forwards `payload` verbatim and never
inspects or reshapes it (see `BuildRecord` in
`services/ingestion-go/internal/telemetry/telemetry.go`). A real inverter
publishes in its vendor's own payload shape, not AmbaGrid telemetry JSON. Per
the core rule above, that payload still needs an adapter to translate it into
the AmbaGrid telemetry model *before* it reaches this topic — pointing this
filter at a vendor's raw, un-normalized output would push it straight into
`telemetry.raw`, bypassing the adapter contract entirely. The config change
only helps once an adapter (see Adapter Targets below) already exists and is
publishing normalized payloads under the new topic.

### Fully wildcarded filter

`MQTT_TOPIC_FILTER=+/+/+/+/telemetry`

Both the region and device-type segments are wildcards, so any value is
accepted at those positions; only the topic structure (5 segments, none
empty, last segment `telemetry`) is enforced. Use this for a bridge instance
meant to ingest telemetry across every region and device type at once.

## Adapter Targets

Good adapter targets include:

- Victron
- SparkMeter
- Schneider Electric
- custom inverter gateways
- national or regional meter providers

Each adapter should handle vendor-specific details close to the edge:

- authentication
- vendor topic names
- unit conversion
- field naming differences
- missing values
- firmware quirks
- retry behavior over weak networks

Adapters should not leak vendor concepts into the core engine unless the concept is truly part of the grid domain.

## Simulator

The local simulator creates fake smart meters across multiple sites:

```bash
go run scripts/virtual_meter.go
```

For a bounded test:

```bash
go run scripts/virtual_meter.go -sites 1 -meters-per-site 2 -interval 1s -duration 5s -log-publishes
```

This lets contributors test the full MQTT to Redpanda path without owning physical solar equipment.
