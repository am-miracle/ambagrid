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
- **api:** Add asset readings endpoint for chart-ready telemetry

### Documentation

- Add open source project foundations
- Add ontology and roadmap docs

### Maintenance

- **github:** Add issue forms (#3)

### Other

- Harden ingestion bridge package split

Move ingestion bridge internals into dedicated packages with focused config, telemetry, stats, and bridge tests. Validate MQTT topic filters before deriving constraints, cover queue shutdown/resubscribe behavior, and document simulator payload fields. Harden the virtual meter simulator state model and add tests for generated readings.

