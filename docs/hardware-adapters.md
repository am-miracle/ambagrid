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
```

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
