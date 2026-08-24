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
- `TariffPlan`
- `Payment`
- `EnergyCredit`
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

The tariff calculation should record the tariff version used at the time of credit issuance. Later tariff changes must not rewrite historical payment or credit records.

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
  -> CreditBalance increases
  -> future payments repay the emergency credit first
```

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
