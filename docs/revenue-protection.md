# Revenue Protection

Mini-grids are commercial businesses. Technical uptime matters, but cash flow keeps the lights on.

Revenue protection is the part of AmbaGrid that keeps electricity access aligned with payment, tariff, credit, meter state, and operator policy.

## Core Question

The system should always be able to answer:

```text
Is this customer allowed to consume power right now?
```

That answer depends on linked operational facts:

```text
Customer
  -> Household
  -> MeterAssignment
  -> SmartMeter
  -> TariffPlan
  -> Payment
  -> EnergyCredit
  -> CreditBalance
  -> MeterCommand
```

## Required Objects

Revenue protection depends on these ontology objects:

- `Customer`
- `Household`
- `SmartMeter`
- `MeterAssignment`
- `TariffPlan`
- `Payment`
- `EnergyCredit`
- `EmergencyCreditAdvance`
- `CreditBalance`
- `MeterCommand`
- `AuditEvent`

## Payment Flow

All payment providers should feed the same internal action.

```text
Provider webhook
  -> verify provider signature
  -> normalize provider payload
  -> ApplyPayment
  -> create Payment
  -> calculate EnergyCredit
  -> update CreditBalance
  -> create ReconnectMeter command if needed
  -> write audit trail
```

This keeps provider-specific code at the edge and billing logic inside AmbaGrid.

## Provider Adapters

Initial provider candidates:

- Paystack or Flutterwave for developer-friendly testing
- OPay or Moniepoint for West Africa
- M-Pesa for East Africa
- Orange Money for Francophone Africa
- MTN MoMo and Airtel Money where operators need them

Provider adapters should handle:

- webhook signature verification
- provider reference parsing
- duplicate webhook detection
- provider status mapping
- currency and settlement metadata
- retry-safe idempotency keys

Provider adapters should not decide whether a meter reconnects. That belongs to the core revenue policy.

## Idempotency

Payment processing must be idempotent.

The same provider webhook may arrive more than once. AmbaGrid should treat the provider reference as a unique external event and avoid issuing credit twice.

Required checks:

- provider name
- external reference
- amount
- currency
- customer or meter mapping
- provider status

## Tariffs and Credit

Tariff plans convert payment into credit.

Example:

```text
NGN 5,000 payment
price_per_kwh = NGN 250
energy credit = 20 kWh
```

The tariff calculation should record the tariff version used at the time of credit issuance. Each `tariff_plans` row is one immutable calculation version. Changing its currency, price, standing charge, site, or start time requires a new row with a new `tariff_plan_id`; `effective_to` may close the old version. This keeps later tariff changes from rewriting historical payment or credit records.

## Emergency Credit

Emergency credit can be valuable in rural deployments, but it is still a financial product. It should be explicit, limited, and auditable.

Suggested policy fields:

- enabled or disabled
- maximum emergency credit
- customer eligibility
- repayment priority
- expiry rule
- operator override permission

Example flow:

```text
Customer requests emergency credit
  -> eligibility is checked
  -> EnergyCredit is issued with source_type=emergency_credit
  -> EmergencyCreditAdvance records the debt
  -> CreditBalance increases
  -> future payments repay the emergency credit first
```

The advance is the part that makes the last step possible. `energy_credits` only grants — `kwh_granted` must be positive — and a credit balance cannot go negative, so an advance recorded only as a grant is indistinguishable from a promotion and every later payment would be docked for the same debt forever. `emergency_credit_advances` carries `outstanding_kwh` down to zero, and `emergency_credit_repayments` records which payment cleared what. An advance pins its tariff plan at issuance, so a tariff change does not move what the customer owes.

## Split Billing

Some loads are shared by a group, such as a water pump, cold room, mill, clinic, or community facility.

Split billing should model:

- shared meter
- participating customers
- contribution rule
- amount due per participant
- paid and unpaid participant state
- operator override

Avoid hiding this inside notes or manual spreadsheets. Shared infrastructure is common enough to deserve a clean model.

## Meter Reassignment

A meter is not assigned by a column. `meter_assignments` records one row per period a customer is billed for a meter, and reassignment closes the open row and opens a new one. Energy credits and credit balances reference `assignment_id`, and everything identifying an assignment except `ended_at` is immutable, so credits issued under the old assignment keep their customer, meter, and site by construction rather than by convention.

The distinction worth keeping in mind: site membership is a property, because a customer does not move between sites, and it travels through composite foreign keys everywhere in this schema. Assignment is an event — it starts, it ends, and its history is worth keeping — so it gets a table rather than a column. A partial unique index allows one open assignment per meter today; relaxing that index is how split billing will later let several customers share one meter.

A meter can leave service without anyone touching money. Closing an assignment does not require deleting the customer's balance, so pulling a faulty meter for repair no longer writes off credit the customer paid for — the balance stays on the closed assignment until an operator settles or transfers it. Deliberately, nothing stops an assignment closing while a balance is still open: gating a hardware action on a financial one is what caused the write-off. A balance left on a closed assignment is a reconciliation item, not a database error.

The one hard guard is on destroying value: a `credit_balances` row holding kWh or money cannot be deleted. Writing off a balance is a decision that should leave a record, so the operator has to zero it deliberately rather than have a reassignment delete it as a side effect.

## Meter Commands

Revenue protection eventually becomes a field action.

Common command types:

- `CreditMeter`
- `DisconnectMeter`
- `ReconnectMeter`
- `RequestMeterStatus`

Command lifecycle:

```text
requested
  -> queued
  -> sent
  -> acknowledged
  -> failed
  -> expired
```

Every command should record:

- who or what requested it
- why it was requested
- which meter it targeted
- when it was sent
- whether the device acknowledged it
- failure reason if delivery failed

## STS-Compatible Vending

Some prepaid meters use STS tokens. Others use vendor APIs, relay commands, or proprietary credit protocols.

AmbaGrid should support STS-compatible vending integration, not casually claim free STS token generation.

STS involves:

- licensing
- vending keys
- meter manufacturer configuration
- key revision numbers
- secure key storage
- operator authorization

Safe product boundary:

```text
AmbaGrid integrates with STS-compatible vending systems and can support operator-owned vending where the operator has the required keys, licenses, and legal authority.
```

The internal ontology should still use the same object:

```text
MeterCommand
```

The adapter decides whether the command becomes an STS token, vendor API request, MQTT message, or manual field workflow.

## Reconciliation

Operators need to find commercial failures quickly.

Reconciliation views should show:

- payment confirmed but no credit issued
- credit issued but meter not updated
- reconnect command sent but not acknowledged
- customer paid wrong account
- duplicate provider reference
- meter consuming while balance is exhausted
- meter disconnected while customer has positive balance

## First Build Target

The first revenue-protection implementation should prove this loop:

```text
Fake payment event
  -> ApplyPayment
  -> Payment record
  -> EnergyCredit record
  -> CreditBalance update
  -> ReconnectMeter or DisconnectMeter command event
  -> audit trail
```
