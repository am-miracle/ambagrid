# Command delivery and vendor integration validation

## MQTT v5 and register-map follow-up — 2026-10-26

The uncommitted MQTT v5, YAML Modbus map, ledger fallback and exact STS decimal
changes were checked against the adapter requirements before committing.

- API: `go vet ./...` and `go test -race ./...` passed, including PostgreSQL
  integration tests against a freshly migrated `ambagrid_review_20261026` database.
- Edge: `go vet ./...` and `go test -race -tags=integration ./...` passed.
  The full MQTT and Modbus packages also passed five repeated race-enabled runs.
- `deploy/mqtt/test-acl.sh` passed against a disposable Mosquitto TLS broker
  using the production configuration, ACL and MQTT v5 request/response topics.
- New real TCP tests reproduced cancellation delays during active Modbus reads
  and while queued, including deadlines. The fix uses context-aware acquisition,
  closes the raw socket when the context ends, and joins that cancellation callback
  before allowing the next exchange. The configured timeout also bounds dialing.
- Topic validation now rejects command/reply collisions after site expansion,
  including collisions that depend on device IDs. The expanded-topic regression
  failed before the validation fix.
- Subscription restoration retries transient failures with bounded attempts and
  stops on disconnect or shutdown. Connection generation and readiness transitions
  share a lock, preventing stale restoration from marking a disconnected session
  ready. Removing either retry or the generation guard made its test fail; the
  source was restored byte-for-byte afterward.
- The in-process broker helper drops sockets and removes its listener before
  `Server.Close`, avoiding mochi's recursive client-list lock while still stopping
  its background loops. Repeated race runs completed without hangs.
- Standards review: 0 remaining actionable findings. Spec review: 0 remaining
  actionable findings after fixing cancellation, topic collisions and readiness.

This follow-up did not rerun the earlier ten reported bug injections or the Rust
suite; Rust code and migrations are unchanged. Physical hardware and a live
PrismVend installation still require commissioning.

Validated 2026-10-25, including the existing uncommitted command-delivery WIP
and the Victron telemetry / PrismVend manual vending additions.

## Checks

- Root Go, api-go, ingestion-go and edge-agent-go: `go vet ./...` and full
  `go test -race ./...` suites passed. Edge tests included `-tags=integration`.
- API PostgreSQL integration tests passed, including concurrent STS reservations,
  replay after service restart, request/installation conflicts and uncertain vends.
- Rust: `cargo fmt --check`, `cargo clippy --all-targets --all-features -- -D warnings`,
  and `cargo test -- --include-ignored --test-threads=1` passed: 95 tests, none ignored.
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

Publications already handed to the MQTT client or broker cannot be recalled by context
cancellation. Devices requiring hard receipt-time expiry must enforce it locally;
the present MQTT protocol has no broker-side message expiry.

## Follow-up: production prerequisites and STS funding

- Added a dedicated TLS/password/ACL command-broker deployment. A disposable
  broker using the actual policy passed authorized traffic, rejected anonymous
  access, cross-site reads/writes, and forged gateway/sibling-device replies.
- Command MQTT now requires TLS, credentials and a CA by default; insecure
  development requires an explicit opt-in. The anonymous dev broker binds to
  loopback and is not a production command broker.
- STS vends require one confirmed, STS-routed payment, with the payment UUID as
  provider reference. PostgreSQL snapshots delivery mode, enforces unique funding,
  checks amount/currency/meter/installation, and blocks duplicate kWh delivery.
- Added audited unresolved listing, token recovery and terminal non-issuance
  tooling. Reconciliation cannot overwrite a resolved result or race an active
  vending owner. It never automatically retries a provider call.
- Missing optional Victron sensors stay absent in protobuf/engine readings.
  Voltage-only fixtures pass the shared suite, and a missing temperature cannot
  resolve an existing overheat incident.

The follow-up API, ingestion and edge Go race suites and full Rust suite passed.
PostgreSQL tests cover concurrent reservation, mode changes, recovery audit and
active-vend/reconciliation ownership. Review found and fixed two races (active
reconciliation and an inconsistent mode snapshot); follow-up review was clear.
Standards: 0 remaining findings. Spec: 0 remaining findings.

## Follow-up: token preservation, retry classification and compensation

- Reproduced issued-token loss on persistence failure, blank CLI output on that
  error, pre-send marking while MQTT was disconnected, an active vend classified
  as uncertain, connection refusal classified as uncertain, and non-issuance
  retaining credited balance. Each targeted regression failed before its fix.
- The service preserves issued tokens with the save error; CLI stdout retains
  the full result while its exit status remains nonzero.
- MQTT readiness precedes its durable send hook. Broker loss after that boundary
  remains conservatively uncertain, because a publish may have escaped.
- Active vends report `ErrVendInProgress`; definite pre-connection failures
  report `ErrVendNotSent`. Safe retry retains/rechecks the original funding ID;
  a sent request with a lost response still cannot be automatically retried.
- Non-issuance atomically records an exact compensating credit and adjusts the
  assignment balance. Other payments remain untouched. Insufficient balance
  rolls back resolution/compensation; duplicate compensation is rejected.
- Previously closed non-issued funded vends can be listed and repaired without
  replacing their provider outcome or original timestamp.
- The filesystem headroom test uses a pinned free-space provider, checks refusal
  and admission deterministically, and passed 20 runs plus repeated race runs.

Review identified the upgrade/repair requirement for old non-issuance records;
it is covered by a persisted-state integration regression.

Final validation for this follow-up: full API and edge Go race/integration suites,
API/edge `go vet`, all 95 Rust tests including PostgreSQL, Rust formatting/Clippy,
and the real TLS broker ACL check passed. The deterministic filesystem test also
caught a deliberately disabled projected-headroom guard; source was restored and
the test passed again. Standards: 0 remaining findings. Spec: 0 remaining findings.
