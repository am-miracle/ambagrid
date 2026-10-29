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

Unknown capability keys fail startup.

## MQTT Command Protocol

Production use requires [broker access control](mqtt-command-security.md).

The MQTT command adapter (`EDGE_COMMAND_ADAPTER=mqtt`) uses protocol v2:
MQTT v5 request/response with JSON payloads at QoS 1 and no retained messages.
MQTT 3.1.1 protocol v1 is unsupported.

A prepaid-credit MQTT device must declare `idempotent_refs: true` and durably
apply each correlation value once, including broker-level redelivery. The adapter refuses
prepaid-credit declarations without this guarantee. QoS 1 alone does not prevent
double credit. A publish already handed to Paho/broker may finish after the caller
stops waiting; cancellation cannot recall it. Such outcomes remain unresolved in
the ledger. Devices requiring a hard receipt-time expiry must enforce it locally;
this protocol does not provide broker-side expiry.

Topics come from a configured topic map. `EDGE_MQTT_COMMAND_TOPIC` and `EDGE_MQTT_REPLY_TOPIC` are templates using `{site_id}` and `{device_id}`; `{device_id}` must fill a whole topic level, and the defaults are `site/{site_id}/meter/{device_id}/cmd` and `site/{site_id}/meter/{device_id}/cmd/reply`.

The gateway subscribes to every device's reply topic before it publishes anything. Expanded command and reply topic sets must not overlap. After reconnecting, commands remain blocked until reply subscriptions have been restored; transient restoration failures are retried. A request goes to the command topic with `ResponseTopic` set to that device's reply topic and `CorrelationData` set to the platform command ID, so a retry is the same request:

```json
{"op": "credit", "device": "meter-01", "wh": 12500}
{"op": "relay", "device": "meter-01", "closed": false}
{"op": "status", "device": "meter-01"}
```

A credit carries either `wh` or an STS `token`. The device publishes its reply to the request's `ResponseTopic`, echoing its `CorrelationData`:

```json
{"ok": true, "ts_ms": 1790865060000, "relay": 1, "balance_wh": 12500}
{"ok": false, "code": "E_METER_LOCKED", "msg": "meter cover open"}
```

`ok` must be present. Only an explicit `"ok": false` records the command as rejected, which the ledger treats as definitely not applied. A reply without `ok`, such as `{}` or `null`, leaves the outcome unknown.

A reply is only accepted on the reply topic of the device the request went to: the command ID is guessable, so the topic, which broker access rules restrict to that device, says who answered. A PUBACK only means the broker has the request; the gateway waits up to `EDGE_MQTT_RESPONSE_TIMEOUT` (default 30s) for the device's reply. A device declaring `outcome_query` also answers `{"op": "outcome", "device": "meter-01", "target": "cmd-0101"}`, sent with its own correlation value, with `{"ok": true, "applied": true}` plus its status fields. Device IDs containing `/`, `+`, or `#` are refused. Fixtures for the suite are in `services/edge-agent-go/internal/adapter/testdata/mqtt/`.

## Adapter Health

The edge exports the following Prometheus series on `/metrics`, independently of
queue-statistics availability. Every series has `adapter` and `device_id` labels;
timeout and unsupported counters also have `operation` (`read` or `command`).

| Metric (all prefixed `ambagrid_edge_adapter_`) | Meaning |
| --- | --- |
| `last_successful_read_timestamp_seconds` | Unix time of the last successful nonempty read |
| `last_successful_command_timestamp_seconds` | Unix time of the last successfully sent and confirmed command |
| `read_errors_total` | Failed read calls, including cancellation |
| `command_errors_total` | Failed command execution calls, including validation, cancellation and unresolved outcomes |
| `protocol_timeouts_total` | Adapter-configured exchange/response timeout; excludes caller deadlines and queued waits |
| `unsupported_total` | Unsupported telemetry fields or commands; missing optional Modbus registers count even when the poll succeeds |

Success gauges start at zero. Counters reset on process restart. Cached ledger
successes and outcome queries do not refresh the successful-command gauge.
Unavailable-value sentinels represent missing data, not unsupported registers,
and do not increment the unsupported counter. Explicit inventory routes use
their configured device ID; the legacy site-wide command route uses `all`.
Command IDs and error text are never metric labels.

Firmware mismatch detection and adapter-health alert rules are not yet implemented.

## Per-device configuration

Set `EDGE_DEVICES_FILE` to a strict YAML inventory to choose adapters independently
for each platform device. A device can have telemetry, commands, or both:

```yaml
devices:
  - id: meter-01
    command: mqtt
    capabilities_file: mqtt/meter-capabilities.example.yaml
  - id: battery-01
    telemetry: modbus
    modbus:
      address: 192.0.2.1:502
      unit_id: 225
      map_file: modbus/victron-battery.yaml
      timeout: 5s
```

This example is shipped as `deploy/devices.example.yaml`; copy it and adjust
addresses, IDs, capabilities and Unit IDs for the installation. Map and
capability paths resolve relative to the inventory. Unknown keys, duplicate IDs,
unknown adapters and incomplete settings fail startup. MQTT credentials, broker
URL, TLS policy, topic templates and response timeout remain in `EDGE_MQTT_*`.
All MQTT devices share one connection but use separate reply subscriptions and
per-device capability declarations. Commands for unconfigured devices are refused;
they never inherit another device's capabilities or transport. Each route uses
the durable ledger before reaching the device.

Telemetry choices are `modbus` and `simulator`; command choices are `mqtt` and
`simulator`. Omit a choice to disable that operation for the device. The simulator
has fixed telemetry identities `meter-01`, `battery-01`, `inverter-01`, and only
`meter-01` accepts commands; one simulator instance supplies its state.
`EDGE_COMMANDS_ENABLED` still controls platform polling, and the collector interval
still applies to all telemetry sources.

The inventory replaces `EDGE_SIMULATOR_ENABLED=true`, `EDGE_MODBUS_ADDRESS`,
`EDGE_COMMAND_ADAPTER` and `EDGE_MQTT_CAPABILITIES_FILE`; mixing these selection
settings is rejected. Without an inventory, the existing environment-only setup
continues to work. Container deployments must mount the inventory and referenced
maps/capability files into the container with paths that agree.

CI explicitly runs simulator, MQTT and Modbus conformance tests with `-race` and
`-tags=integration`; the regular edge suite also tests runtime routing, metric
classification, cancellation and reconnect behavior.

## Test Fixtures

Every adapter should include fixtures.

Required fixtures:

- sample raw input
- expected normalized telemetry
- sample command request
- expected vendor payload
- error response example

This lets contributors add hardware support without needing physical devices for every change.

## Modbus register maps

The Modbus TCP adapter reads devices through YAML register maps. Set
`EDGE_MODBUS_ADDRESS` (`host:port`), `EDGE_MODBUS_UNIT_ID`, `EDGE_MODBUS_DEVICE_ID`
(the platform asset ID) and `EDGE_MODBUS_MAP_FILE`. Collection runs at
`EDGE_COLLECTOR_INTERVAL` without needing the simulator.

`EDGE_MODBUS_TIMEOUT` (default 5s) bounds each exchange, including dialing.
Cancellation interrupts queued and active reads; an interrupted connection is
reopened on the next read.

```yaml
device_type: battery_bms
manufacturer: victron
model: Cerbo GX battery service
points:
  - field: voltage        # voltage, current, active_power, frequency, total_kwh,
    address: 259          # internal_temperature, battery_soc, solar_irradiance
    function: holding     # holding or input
    type: u16             # u16, i16, u32, i32, f32
    scale: 0.01           # multiplies the raw value into the field's unit
    unit: V               # must match the field: V, A, kW, Hz, kWh, C, %, W/m2
    unavailable: 65535    # raw value meaning "no reading"
  - field: total_kwh
    address: 30
    function: input
    type: u32
    word_order: big       # required for 32-bit types: which register is the high word
    scale: 0.001
    unit: kWh
    optional: true        # a missing or unavailable register is left missing
```

Unknown map keys are rejected. Points are read separately to avoid unsupported
register gaps. An unavailable required point fails the read; an optional point
stays absent. Missing temperature cannot open or clear an overheat incident.
`solar_irradiance` cannot be optional because its absence cannot be represented.
Modbus exceptions 1 and 2 mean an unsupported register; other exceptions mean
the device rejected the read. TCP transport uses
[grid-x/modbus](https://github.com/grid-x/modbus).

### Victron Cerbo GX battery

`deploy/modbus/victron-battery.yaml` maps the GX `com.victronenergy.battery`
service. Voltage is required; the other fields below are optional.

Enable Modbus-TCP and find the battery service's Unit ID under **Available
services** (100 is the system, not the battery). Configure the actual address,
Unit ID and platform asset ID, for example:

```sh
EDGE_MODBUS_ADDRESS=192.0.2.1:502
EDGE_MODBUS_UNIT_ID=225
EDGE_MODBUS_DEVICE_ID=battery-01
```

Timestamps mark GX acquisition, which may return cached battery data. Check
freshness against the installed firmware and battery during commissioning.
The [GX Modbus-TCP manual](https://www.victronenergy.com/live/ccgx:modbustcp_faq)
covers service discovery, functions and missing-register errors. The adapter
uses FC3; GX also supports FC4.
Victron's [server register definitions](https://github.com/victronenergy/dbus_modbustcp/blob/master/attributes.csv)
specify these read-only battery fields; decode signed values before dividing:

| Address | Field | Wire type | Divide by | Unit |
| --- | --- | --- | --- | --- |
| 259 | Battery voltage | uint16 | 100 | V |
| 261 | Battery current | int16 | 10 | A |
| 262 | Battery temperature | int16 | 10 | °C |
| 266 | State of charge | uint16 | 10 | % |

This integration provides telemetry only. Its fixtures use a local Modbus server;
physical hardware compatibility and freshness require commissioning.
