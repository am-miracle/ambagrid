# Roadmap

AmbaGrid is open-source telemetry, billing, and control infrastructure for solar mini-grid operators.

The roadmap is ordered around one constraint: operators need uptime and revenue protection before advanced analytics. Every phase should strengthen that loop.

```text
Telemetry arrives
Payment is confirmed
Credit balance changes
Meter command is issued
Operator can audit the result
```

## Product Thesis

Mini-grid software cannot be only an IoT dashboard.

Operators need to know what the grid is doing, who has paid, which meters should be connected, which assets are failing, and which actions were taken. AmbaGrid should combine technical telemetry and commercial state into one operational system.

## Roadmap Areas

- [Revenue protection](revenue-protection.md): payments, tariffs, prepaid credit, STS-compatible vending, customer balances, and meter commands.
- [Edge reliability](edge-reliability.md): store-and-forward buffering, offline sync, SMS fallback, gateway identity, and OTA update boundaries.
- [Hardware adapters](hardware-adapters.md): meter, inverter, battery, MQTT, Modbus, and vendor-specific integration contracts.
- [Investor reporting](investor-reporting.md): ESG reports, impact metrics, portfolio views, and lender/donor reporting.

## Phases

### Phase 1: Operator Core

Goal: prove the minimum loop that makes AmbaGrid useful to a real operator.

Build:

- customer, household, site, and smart meter records
- payment records
- tariff plans
- energy credits
- credit balances
- meter command lifecycle
- alert lifecycle
- audit trail for payment, credit, and command actions
- simulator events for telemetry and payments
- dashboard views for meter state, customer balance, and alerts

Core flows:

```text
Customer pays
  -> payment is verified
  -> credit is issued
  -> balance updates
  -> reconnect command is created if needed
  -> command delivery is tracked
```

```text
Telemetry arrives
  -> latest meter or asset state updates
  -> abnormal condition is detected
  -> alert opens
  -> operator or automation acts
  -> action is audited
```

### Phase 2: Edge Reliability

Goal: keep AmbaGrid useful when rural networks are unreliable.

Read the detailed plan in [edge-reliability.md](edge-reliability.md).

### Phase 3: Revenue Protection

Goal: make billing, credit, and access control first-class parts of the platform.

Read the detailed plan in [revenue-protection.md](revenue-protection.md).

### Phase 4: Meter Credit and STS Support

Goal: support prepaid metering without locking AmbaGrid to one vendor.

Read the detailed plan in [revenue-protection.md](revenue-protection.md).

### Phase 5: Hardware Interoperability

Goal: make AmbaGrid work across the mixed hardware reality of African mini-grids.

Read the detailed plan in [hardware-adapters.md](hardware-adapters.md).

### Phase 6: Advanced Rural Operations

Goal: reduce field costs and prevent avoidable outages.

Build:

- theft and bypass detection
- abnormal load detection
- battery degradation analytics
- solar production anomaly detection
- automated load-shedding policy
- maintenance ticket workflow
- technician dispatch notes

Keep deterministic safety logic separate from machine-learning output:

```text
Analytics service predicts risk
Rust engine applies deterministic policy
Operator sees the reason and audit trail
```

### Phase 7: Investor and Compliance Reporting

Goal: give operators, lenders, donors, and governments reliable portfolio-level reporting.

Read the detailed plan in [investor-reporting.md](investor-reporting.md).

## Near-Term Milestones

### Milestone 1: Revenue-Aware Local Demo

Expected result:

```text
Run Docker Compose
Run simulator
Inject fake payment
See credit balance update
See reconnect/disconnect command event
See everything in Redpanda/Postgres/dashboard
```

### Milestone 2: Payment Adapter Skeleton

Expected result:

```text
One provider adapter
Webhook signature verification
Idempotent ApplyPayment action
Payment audit trail
Credit issuance
```

### Milestone 3: Edge Agent Prototype

Expected result:

```text
Local disk queue
Network outage simulation
Replay after recovery
Queue status metrics
Critical alert fallback design
```

### Milestone 4: Hardware Adapter Contract

Expected result:

```text
Simulated meter adapter
MQTT command adapter
Modbus adapter shape
Adapter conformance tests
```

## What Not To Build Yet

Avoid these until the operator core is working:

- generic AI assistant features
- unsupported claims about free STS vending
- a large hardware driver catalog without adapter contracts
- complex financial products
- custom dashboards for every funder
- deep forecasting before basic event quality is proven

## Roadmap Rule

If a feature does not improve uptime, revenue protection, hardware interoperability, or operator accountability, it waits.
