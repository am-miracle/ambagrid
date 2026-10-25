# Hardware Adapters

African mini-grid operators use a mixed hardware stack. A single deployment may include Chinese smart meters, European inverters, American battery systems, local gateways, and custom field wiring.

AmbaGrid should not bake vendor behavior into the core engine. It should define stable adapter contracts and let vendor-specific integrations live at the edge.

## Adapter Principle

The core platform should speak AmbaGrid concepts:

```text
SmartMeter
Asset
TelemetryReading
MeterCommand
Alert
```

Adapters should translate between those concepts and vendor protocols:

```text
Modbus registers
MQTT topics
HTTP APIs
serial frames
STS vending systems
vendor cloud APIs
```

## Adapter Types

### Meter Adapter

Reads consumption and relay state from a smart meter.

Common outputs:

- voltage
- current
- active power
- total kWh
- relay state
- tamper status
- last seen time

Common commands:

- credit meter
- disconnect
- reconnect
- request status

### Inverter Adapter

Reads production, load, and device health from an inverter or charge controller.

Common outputs:

- AC output power
- solar input power
- battery charge/discharge power
- frequency
- operating mode
- fault code

### Battery Adapter

Reads battery management system state.

Common outputs:

- state of charge
- voltage
- current
- temperature
- cycle count
- fault state

### Command Adapter

Delivers actions to field hardware.

Delivery mechanisms may include:

- MQTT command topic
- vendor HTTP API
- local serial command
- STS token generation or vending integration
- manual field-agent workflow

## Target Hardware Families

Initial adapter research should focus on equipment commonly seen in African mini-grid deployments:

- Victron
- Deye
- Growatt
- Must
- Solis
- Hexing
- Sanxing
- SparkMeter

This list is not a promise that all drivers exist. It is the target surface for community and operator-driven integrations.

## Configuration Maps

Adapters should be configurable without recompiling the platform.

Useful configuration files:

- Modbus register maps
- MQTT topic maps
- unit conversion rules
- device capability declarations
- command templates
- fault code maps

Example capability declaration:

```yaml
device_type: smart_meter
manufacturer: hexing
model: example
capabilities:
  telemetry: true
  relay_control: true
  prepaid_credit: true
  sts_token: true
  idempotent_refs: false
  outcome_query: false
```

`idempotent_refs` declares that the device ignores or rejects a repeated command reference. It decides whether a command whose outcome is unknown can be resent safely; see [the command ledger](command-ledger.md).

`outcome_query` declares that the device can answer whether it applied a given command reference, which lets an unknown outcome be settled without resending.

Unknown keys are rejected when the declaration is loaded, so a misspelled capability fails at startup instead of reading as `false`.

## MQTT Command Protocol

The MQTT command adapter (`EDGE_COMMAND_ADAPTER=mqtt`) talks to any device or bridge that speaks this protocol, version 1. Messages are JSON at QoS 1, not retained.

A prepaid-credit MQTT device must declare `idempotent_refs: true` and durably
apply each reference once, including broker-level redelivery. The adapter refuses
prepaid-credit declarations without this guarantee. QoS 1 alone does not prevent
double credit. A publish already handed to Paho/broker may finish after the caller
stops waiting; cancellation cannot recall it. Such outcomes remain unresolved in
the ledger. Devices requiring a hard receipt-time expiry must enforce it locally;
this protocol does not provide broker-side expiry.

The gateway publishes a request to `{prefix}/{device_id}/command`, where `{prefix}` is `EDGE_MQTT_TOPIC_PREFIX` (default `ambagrid/{site_id}`). `ref` is the platform command ID:

```json
{"ref": "cmd-0101", "op": "credit", "device": "meter-01", "wh": 12500}
{"ref": "cmd-0102", "op": "relay", "device": "meter-01", "closed": false}
{"ref": "cmd-0103", "op": "status", "device": "meter-01"}
```

A credit carries either `wh` or an STS `token`. The device replies on `{prefix}/{device_id}/command/reply` with the same `ref`:

```json
{"ref": "cmd-0101", "ok": true, "ts_ms": 1790865060000, "relay": 1, "balance_wh": 12500}
{"ref": "cmd-0101", "ok": false, "code": "E_METER_LOCKED", "msg": "meter cover open"}
```

Replies are only accepted on the reply topic of the device the request went to. A device declaring `outcome_query` also answers `{"ref": "<query ref>", "op": "outcome", "device": "meter-01", "target": "cmd-0101"}` with `{"ref": "<query ref>", "ok": true, "applied": true}` plus its status fields. Device IDs containing `/`, `+`, or `#` are refused. Fixtures for the suite are in `services/edge-agent-go/internal/adapter/testdata/mqtt/`.

## Adapter Health

Operators need to know when an adapter is failing.

Track:

- last successful read
- last successful command
- read error rate
- command failure rate
- unsupported fields
- firmware mismatch
- protocol timeout

Adapter failures should create alerts when they affect operations or revenue.

## Test Fixtures

Every adapter should include fixtures.

Required fixtures:

- sample raw input
- expected normalized telemetry
- sample command request
- expected vendor payload
- error response example

This lets contributors add hardware support without needing physical devices for every change.

## First Build Target

Build the adapter contract before building a large driver library.

First targets:

- simulated meter adapter
- MQTT command adapter
- Modbus read adapter shape
- adapter conformance tests

The core should stay stable while hardware support grows around it.

## Victron Cerbo GX battery telemetry

The edge collector supports the GX `com.victronenergy.battery` Modbus-TCP
service. Enable Modbus-TCP on the Cerbo GX, then use **Available services** to
find the battery service Unit ID. Set `EDGE_VICTRON_ADDRESS=192.0.2.1:502`,
`EDGE_VICTRON_UNIT_ID=225` (replace with the actual ID), and
`EDGE_VICTRON_DEVICE_ID=battery-01` (the platform asset ID).
Collection runs at `EDGE_COLLECTOR_INTERVAL`; simulator collection may remain disabled.

The driver individually reads registers 259 (V /100), 261 (signed A /10),
262 (signed °C /10), and 266 (SoC % /10). It supports telemetry only. It does
not control GX relays, vend credit, or equate inverter shutdown with meter disconnect.
Timestamps describe successful GX acquisition; GX may hold cached battery data,
so they do not prove the underlying battery's last physical communication time.
Commission against the configured firmware/battery before using freshness alerts.

See [verified vendor documentation and register sources](vendor-integration-research.md).
