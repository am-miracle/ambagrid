# Ontology

AmbaGrid should use an ontology as its operational model: a clear map of the real-world objects, relationships, actions, and policies that make a mini-grid business work.

The goal is not to copy Palantir Foundry. The goal is to apply the useful idea: raw data becomes useful when it is tied to objects people operate, actions the system can take, and rules that protect safety and revenue.

## Why This Matters

Telemetry alone does not run a mini-grid business.

Operators need to know more than voltage, current, frequency, and temperature. They need to know which customer owns the meter, whether that customer paid, which tariff applies, whether a reconnect command was sent, and whether the meter acknowledged it.

The ontology is the layer that connects those facts.

```text
Raw telemetry says:
  met-0104 reports 219 V and relay_closed=true

The ontology says:
  Smart Meter met-0104 serves Household house-0104
  Household house-0104 belongs to Customer cust-0104
  Customer cust-0104 has no remaining credit
  Therefore AmbaGrid may create a DisconnectMeter command
```

That is the difference between monitoring and operating.

## Product Principle

AmbaGrid should model the grid the way operators think about it.

```text
Objects are the nouns.
Links are the relationships.
Actions are the verbs.
Policies define who or what is allowed to act.
Events record what happened.
```

## Core Objects

These are the first ontology objects AmbaGrid should standardize.

### Grid Operations

**Site**

The physical mini-grid location. A site groups equipment, customers, telemetry, outages, alerts, and revenue.

Useful properties:

- `site_id`
- `name`
- `country`
- `region`
- `operator_id`
- `status`

**Asset**

A physical piece of equipment at a site. Batteries, inverters, solar arrays, feeders, and meters are assets.

Useful properties:

- `asset_id`
- `site_id`
- `asset_type`
- `manufacturer`
- `model`
- `serial_number`
- `status`

**SmartMeter**

The customer-side metering and control device. A smart meter measures consumption and may support relay commands or prepaid credit.

Useful properties:

- `meter_id`
- `site_id`
- `household_id`
- `customer_id`
- `serial_number`
- `relay_state`
- `last_seen_at`
- `firmware_version`
- `supports_sts`

**Household**

The physical premise receiving electricity.

Useful properties:

- `household_id`
- `site_id`
- `meter_id`
- `address_label`
- `connection_status`

**Alert**

A condition that needs operator or automated attention.

Useful properties:

- `alert_id`
- `site_id`
- `asset_id`
- `kind`
- `severity`
- `status`
- `reason`
- `opened_at`
- `resolved_at`

**Outage**

A supply interruption affecting a meter, feeder, or site.

Useful properties:

- `outage_id`
- `site_id`
- `scope`
- `started_at`
- `ended_at`
- `customer_count`
- `suspected_cause`

### Commercial Operations

**Customer**

The person, business, or organization responsible for payment.

Useful properties:

- `customer_id`
- `display_name`
- `phone_number`
- `site_id`
- `status`

**TariffPlan**

The pricing rule used to convert payment into energy credit or charges.

Useful properties:

- `tariff_plan_id`
- `site_id`
- `currency`
- `price_per_kwh`
- `standing_charge`
- `effective_from`
- `effective_to`

**Payment**

A confirmed money movement from a customer or agent.

Useful properties:

- `payment_id`
- `provider`
- `external_reference`
- `customer_id`
- `amount`
- `currency`
- `status`
- `confirmed_at`

**EnergyCredit**

The electricity value granted from a payment, adjustment, promotion, or operator correction.

Useful properties:

- `credit_id`
- `customer_id`
- `meter_id`
- `source_type`
- `source_id`
- `kwh_granted`
- `created_at`

**CreditBalance**

The current remaining commercial entitlement for a customer or meter.

Useful properties:

- `customer_id`
- `meter_id`
- `remaining_kwh`
- `remaining_money_value`
- `updated_at`

**MeterCommand**

An instruction that must be delivered to field hardware.

Useful properties:

- `command_id`
- `meter_id`
- `command_type`
- `status`
- `requested_by`
- `requested_at`
- `acknowledged_at`
- `failure_reason`

## Core Links

The first ontology links should be explicit and typed.

```text
Site owns Asset
Site serves Household
Site has TariffPlan

Customer pays Payment
Customer receives EnergyCredit
Customer has CreditBalance
Customer occupies Household

Household uses SmartMeter
SmartMeter belongs to Site
SmartMeter reports TelemetryReading
SmartMeter receives MeterCommand

Payment creates EnergyCredit
EnergyCredit changes CreditBalance

Alert affects Asset
Alert may create MeterCommand
Outage affects Site
Outage affects Household
```

Avoid hiding these relationships inside free-form JSON. If a relationship affects safety, revenue, or field operations, it should be modeled as a first-class link.

## Core Actions

Actions are governed operations. They are not just API handlers. Every action should produce an audit trail and should make its state transition clear.

### ApplyPayment

Used when a mobile-money provider, payment processor, or field agent confirms payment.

Inputs:

- `provider`
- `external_reference`
- `customer_id`
- `amount`
- `currency`
- `confirmed_at`

Effects:

- creates or updates `Payment`
- calculates energy value from `TariffPlan`
- creates `EnergyCredit`
- updates `CreditBalance`
- may request `ReconnectMeter`

### IssueCredit

Used when the operator grants credit manually or when a non-payment credit source is approved.

Inputs:

- `customer_id`
- `meter_id`
- `kwh_granted`
- `reason`
- `requested_by`

Effects:

- creates `EnergyCredit`
- updates `CreditBalance`
- writes audit record

### DisconnectMeter

Used when a meter should stop supplying electricity.

Inputs:

- `meter_id`
- `reason`
- `requested_by`

Effects:

- creates `MeterCommand`
- sends command to the hardware adapter
- records delivery and acknowledgement state

### ReconnectMeter

Used when a customer regains permission to consume power.

Inputs:

- `meter_id`
- `reason`
- `requested_by`

Effects:

- creates `MeterCommand`
- sends command to the hardware adapter
- records delivery and acknowledgement state

### OpenAlert

Used when telemetry, payment, device, or command state indicates a problem.

Inputs:

- `site_id`
- `asset_id`
- `kind`
- `severity`
- `reason`
- `source_event_id`

`kind` identifies the problem (e.g. `internal_temperature`), distinct from
`reason`'s human-readable, per-reading text. An asset can have at most one
open alert per kind, so distinct problems on the same asset (e.g.
overheating and low battery) get independent alerts instead of one
colliding with the other.

Effects:

- creates `Alert`
- notifies operators or automation

### ResolveAlert

Used when the problem has been handled.

Inputs:

- `alert_id`
- `resolution_note`
- `resolved_by`

Effects:

- updates `Alert`
- records resolution history

## Revenue Protection Model

Revenue protection should be a core part of the ontology.

The important commercial question is:

```text
Is this customer allowed to consume power right now?
```

That answer depends on several linked facts:

```text
Customer
  -> CreditBalance
  -> TariffPlan
  -> SmartMeter
  -> relay_state
  -> latest consumption
  -> latest payment status
  -> pending meter commands
```

A practical rule:

```text
If CreditBalance.remaining_kwh <= 0
and SmartMeter.relay_state = closed
and no grace policy applies
then create DisconnectMeter
```

Another:

```text
If Payment.status = confirmed
and CreditBalance.remaining_kwh > 0
and SmartMeter.relay_state = open
then create ReconnectMeter
```

This means mobile-money integration is not a side feature. It feeds the same operational model as telemetry.

## Mobile Money and Payment Adapters

Payment providers should be adapters around the same internal action.

```text
M-Pesa webhook
OPay webhook
MTN MoMo webhook
Airtel Money webhook
Paystack webhook
Flutterwave webhook
Cash agent entry
        |
        v
ApplyPayment
        |
        v
Payment -> EnergyCredit -> CreditBalance -> MeterCommand
```

Provider-specific behavior belongs at the edge:

- signature verification
- provider reference parsing
- currency and settlement metadata
- duplicate webhook handling
- provider status mapping

Core billing behavior belongs inside AmbaGrid:

- idempotency
- tariff calculation
- credit issuance
- balance updates
- reconnect decisions
- audit trail

## STS and Smart Meter Support

Some prepaid meters use STS tokens. Others expose vendor APIs, relay commands, or proprietary credit protocols.

AmbaGrid should model these as hardware adapters, not as different billing systems.

```text
IssueCredit
        |
        +--> STS token adapter
        +--> Vendor smart-meter API adapter
        +--> MQTT relay command adapter
        +--> Manual field-agent workflow
```

The ontology object remains the same:

```text
MeterCommand
```

The delivery mechanism can vary.

## Event Topics

The ontology should be fed by stream events. Initial topic names:

```text
telemetry.ingested
payment.confirmed
payment.failed
credit.issued
meter.command.requested
meter.command.acknowledged
alert.opened
alert.resolved
```

The names should describe business events, not service internals.

### Sample alert.opened / alert.resolved payloads

Both are protobuf (`proto/alerts.proto`); shown here as their JSON
equivalent for readability, published by the Rust engine after the
corresponding Postgres write commits.

`alert.opened`:

```json
{
  "alert_id": "b3b3c2b0-6e2a-4d9a-9c3a-1f2e3d4c5b6a",
  "asset_id": "met-0101",
  "site_id": "ng-kaji-01",
  "severity": "SEVERITY_CRITICAL",
  "reason": "internal_temperature_high:72.4C>=threshold:70.0C",
  "opened_at_utc": 1745500000
}
```

`alert.resolved`, once the same alert clears:

```json
{
  "alert_id": "b3b3c2b0-6e2a-4d9a-9c3a-1f2e3d4c5b6a",
  "asset_id": "met-0101",
  "site_id": "ng-kaji-01",
  "severity": "SEVERITY_CRITICAL",
  "reason": "internal_temperature_high:72.4C>=threshold:70.0C",
  "opened_at_utc": 1745500000,
  "resolved_at_utc": 1745500900,
  "resolution_note": "internal_temperature_recovered:64.0C<threshold:65.0C",
  "resolved_by": "system"
}
```

`resolved_by` is `"system"` for the automatic recovery path (a reading
coming back within normal range); an operator-triggered resolution would
carry the operator's identity instead.

## Storage Boundary

Use PostgreSQL and TimescaleDB differently.

PostgreSQL tables should store ontology objects and links:

- sites
- assets
- customers
- households
- smart_meters
- tariff_plans
- payments
- energy_credits
- credit_balances
- meter_commands
- alerts
- outages

TimescaleDB hypertables should store high-frequency measurements:

- voltage
- current
- active power
- frequency
- battery state of charge
- internal temperature
- relay state observations

The ontology should reference telemetry summaries, latest state, and event history. It should not turn every raw metric sample into a graph object.

## Security and Permissions

Actions need explicit permission boundaries.

Suggested roles:

```text
Operator
  can acknowledge alerts
  can request reconnect
  can create maintenance tickets

FinanceAdmin
  can reverse payments
  can issue manual credit
  can change tariff plans

Technician
  can update asset status
  can resolve maintenance alerts

SystemAgent
  can open alerts
  can request disconnect or reconnect under policy

Auditor
  can read payments, credits, commands, and audit trails
  cannot mutate operational state
```

No action that affects money or power supply should happen without an audit record.

## Initial Implementation Boundary

The first implementation should stay small.

Build these first:

- `Site`
- `Customer`
- `Household`
- `SmartMeter`
- `Payment`
- `EnergyCredit`
- `CreditBalance`
- `MeterCommand`
- `Alert`

Build these first actions:

- `ApplyPayment`
- `IssueCredit`
- `DisconnectMeter`
- `ReconnectMeter`
- `OpenAlert`
- `ResolveAlert`

Defer these:

- advanced forecasting
- full maintenance scheduling
- complex tariff experiments
- multi-operator marketplace logic
- closed-source enterprise modules

The first version should prove one commercial loop:

```text
Customer pays
  -> payment is verified
  -> credit is issued
  -> balance updates
  -> meter reconnects if needed
  -> operator can audit the full chain
```

And one operational loop:

```text
Telemetry arrives
  -> asset state updates
  -> unsafe or abnormal condition is detected
  -> alert opens
  -> operator or automation acts
  -> action is audited
```

## Design Rule

If a concept affects safety, uptime, revenue, customer access, or operator accountability, it belongs in the ontology.

If it is only a transport detail, temporary parsing shape, or UI convenience, it does not.
