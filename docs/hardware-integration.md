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
