# Changelog

All notable changes to AmbaGrid will be documented here.

This project follows the spirit of Keep a Changelog. Dates use `YYYY-MM-DD`.

## [Unreleased]

### Added

- Add MQTT to Redpanda ingestion bridge
- Add app and service scaffolding
- **engine:** Add database migrations (#2)
- **telemetry:** Ingest protobuf readings
- **alerts:** Add resolve_alert action and resolution metadata
- **alerts:** Publish alert.opened/alert.resolved events to kafka
- **alerts:** Tune threshold policy per asset type with hysteresis and runtime config
- **kafka:** Migrate consumer to rdkafka for real partition/consumer-group coverage

### Changed

- **engine:** Consolidate ingest writes into one transactional repository
- **engine:** Tighten alert and asset domain types
- **engine:** Use rdkafka for Kafka publishing
- **engine:** Group asset persistence by state

### Documentation

- Add open source project foundations
- Add ontology and roadmap docs

### Fixed

- **telemetry:** Reject incomplete smart-meter metrics and preserve per-field presence
- **alerts:** Prevent duplicate alerts from repeated abnormal readings
- **alerts:** Scope alert open/resolve/duplicate-guard by kind, not just asset
- **engine:** Avoid dynamic librdkafka dependency in CI
- **alerts:** Persist opened alert source events
- **engine:** Publish telemetry failures to Kafka DLQ
- **alerts:** Reject resolved events without timestamps

### Maintenance

- **github:** Add issue forms (#3)

### Other

- Harden ingestion bridge package split

Move ingestion bridge internals into dedicated packages with focused config, telemetry, stats, and bridge tests. Validate MQTT topic filters before deriving constraints, cover queue shutdown/resubscribe behavior, and document simulator payload fields. Harden the virtual meter simulator state model and add tests for generated readings.

