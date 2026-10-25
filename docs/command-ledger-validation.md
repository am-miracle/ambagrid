# Command delivery and vendor integration validation

Validated 2026-10-25, including the existing uncommitted command-delivery WIP
and the Victron telemetry / PrismVend manual vending additions.

## Checks

- Root Go, api-go, ingestion-go and edge-agent-go: `go vet ./...` and full
  `go test -race ./...` suites passed. Edge tests included `-tags=integration`.
- API PostgreSQL integration tests passed, including concurrent STS reservations,
  replay after service restart, request/installation conflicts and uncertain vends.
- Rust: `cargo fmt --check`, `cargo clippy --all-targets --all-features -- -D warnings`,
  and `cargo test -- --include-ignored --test-threads=1` passed: 94 tests, none ignored.
- Go formatting, `git diff --check`, and `docker compose config --quiet` passed.
- Regenerated telemetry Go bindings with the installed protoc/protoc-gen-go.

The existing development database rejected an old migration checksum. Tests
used a separate local `ambagrid_review_20261025` database migrated from scratch;
no development data was rewritten to bypass that mismatch.

Three regression checks were observed failing with the safeguard absent:

1. Runner execution deadline extending beyond platform command expiry.
2. MQTT accepting prepaid credit without receiver reference deduplication.
3. Retrying an uncertain PrismVend request (provider call count increased to two).

Mutations were restored, and the affected tests passed again. This validates
these three regressions; it does not independently reproduce the earlier report
of eleven bug injections.

## Standards

No remaining actionable findings. Transport, services, repositories and entry
points preserve their documented responsibilities. No baseline smell warranted
a finding.

## Spec

Two findings were fixed during review: MQTT QoS 1 required durable receiver-side
reference deduplication for prepaid credit; command execution needed a deadline
bounded by platform expiry, with cancellation checked before transmission.
Follow-up review found no remaining actionable findings.

Standards: 0 remaining findings. Spec: 0 remaining findings.

## Commissioning limits

Victron tests use manufacturer register definitions and a local Modbus server;
PrismVend tests use its published HTTP contract and a local HTTP server. No
physical battery, licensed Security Module, production keys, provider tariff or
customer meter was accessed. Automatic payment-to-token dispatch is not wired;
the STS command implements a separate durable manual vending workflow.

Paho/broker publications already in flight cannot be recalled by context
cancellation. Devices requiring hard receipt-time expiry must enforce it locally;
the present MQTT protocol has no broker-side message expiry.
