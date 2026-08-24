# Architecture

AmbaGrid is built as a real-time pipeline. Field devices send telemetry, the platform makes that telemetry durable, and services consume it for rules, storage, APIs, and operator dashboards.

## Data Flow

```text
Solar assets, smart meters, batteries, inverters
        |
        | MQTT telemetry
        v
Mosquitto MQTT broker
        |
        | africa-west/+/smartmeter/+/telemetry
        v
Go ingestion bridge
        |
        | Kafka records
        v
Redpanda topic: telemetry.raw
        |
        +------> Rust engine
        |          - grid rules
        |          - digital twin state
        |          - fast control decisions
        |
        +------> TimescaleDB / PostgreSQL
        |          - time-series telemetry
        |          - sites, meters, households
        |
        v
Go API
        |
        v
React / TypeScript control room
```

## Why These Pieces Exist

Go handles ingestion. The Go bridge catches MQTT telemetry from field devices and forwards it into Redpanda without doing heavy work in the MQTT callback path.

Redpanda is the durable stream. It stores telemetry as Kafka-compatible records so downstream services can process data at their own pace.

Rust is the control engine. It is the right place for low-latency rules, graph traversal, forecasting, and grid-state decisions.

PostgreSQL with TimescaleDB stores operational data and high-frequency metrics. Normal relational tables hold sites, households, devices, and users. Hypertables should hold voltage, current, power, frequency, and battery readings.

TypeScript and React power the control room. The frontend is where operators see live state, alerts, maps, and battery health.

## Scaling Model

AmbaGrid scales by keeping the ingestion layer stateless.

If more meters come online, run more Go ingestion containers. These containers do not own durable state; they receive MQTT messages, add stream metadata, and publish Kafka records.

Redpanda can split traffic across partitions. A practical partitioning key is region, site ID, or device ID, depending on the processing model:

- region partitions help separate large geographic lanes
- site partitions keep one mini-grid's telemetry together
- device partitions preserve per-meter ordering

The current bridge uses device ID as the Kafka key. That keeps readings from the same meter ordered on the same partition.

TimescaleDB should split slow-changing operational data from high-frequency telemetry. Sites, tariffs, customers, and device inventory belong in normal relational tables. Meter readings belong in hypertables.

## Community Scaling

The simulator is part of the architecture, not a toy.

Most contributors will not have real meters, inverters, or batteries on their desk. The Go simulator creates realistic traffic so contributors can build ingestion, storage, dashboards, and engine rules without physical hardware.

That makes it possible for engineers anywhere to contribute with the same local setup.

## Business Model

The code is open. The business is convenience, reliability, and expertise.

AmbaGrid Cloud can offer a hosted version for operators that do not want to run streaming infrastructure, certificates, backups, and dashboards themselves.

Enterprise modules can add paid capabilities around forecasting, billing reconciliation, telecom wallet integrations, and advanced fleet analytics.

Integration work can help ministries, utilities, and large operators deploy AmbaGrid inside existing national or regional systems.
