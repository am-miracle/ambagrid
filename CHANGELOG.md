# Changelog

All notable changes to AmbaGrid will be documented here.

This project follows the spirit of Keep a Changelog. Dates use `YYYY-MM-DD`.

## [Unreleased]

### Added

- Add MQTT to Redpanda ingestion bridge
- Add app and service scaffolding
- **engine:** Add database migrations (#2)
- **telemetry:** Ingest protobuf readings (#6)
- Telemetry ingestion pipeline with alert lifecycle and horizontal scaling (#9)
- **api:** Add read API with alert query safeguards (#10)
- **api:** Add asset readings endpoint for chart-ready telemetry (#11)
- **api:** Add alert resolution command
- **domain:** Separate GridOperator from Actor (#13)
- **ui:** Build operator control room (#15)
- **engine:** Add revenue protection storage schema (#16)
- Add authenticated staging telemetry worker (#17)
- Add prepaid payment processing (#18)
- Add revenue operations dashboard (#19)
- **edge:** Add durable telemetry and critical fallback (#21)
- **adapters:** Deliver durable commands and add Victron and STS integrations
- **edge:** Use MQTT v5 requests and mapped Modbus telemetry
- **edge:** Add adapter health metrics and per-device wiring

### Documentation

- Add open source project foundations
- Add ontology and roadmap docs
- Regenerate changelog
- **adapters:** Document MQTT v5 migration and validation

### Fixed

- **outbox:** Publish alert events durably (#12)
- **engine:** Make outbox dependencies explicit (#14)
- **adapters:** Enforce funded STS delivery and production MQTT isolation
- **adapters:** Preserve vending results and reverse undelivered credits
- **edge:** Record commands applied without the send hook
- **sts:** Reverse what the balance holds and record shortfalls
- **adapters:** Retain applied results through storage outages
- **deps:** Patch ctutils constant-time advisory

### Maintenance

- **github:** Add issue forms (#3)

### Other

- Harden ingestion bridge package split

Move ingestion bridge internals into dedicated packages with focused config, telemetry, stats, and bridge tests. Validate MQTT topic filters before deriving constraints, cover queue shutdown/resubscribe behavior, and document simulator payload fields. Harden the virtual meter simulator state model and add tests for generated readings.

### Testing

- **api:** Tolerate database clock skew in the expiry bound

