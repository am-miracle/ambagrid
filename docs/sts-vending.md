# STS vending with PrismVend

The platform integrates PrismVend's VendCredit2 API revision 5.22.2; see
[verified protocol documentation](vendor-integration-research.md). Provision the
Security Module, keys, meter registration, tariff and currency first. The provider
service has no built-in authentication: use a secured operator network or an
authenticated gateway. No provider token-retrieval endpoint is assumed.

## One physical credit route per payment

Apply all engine migrations, through `202610270001_sts_funding_visibility.sql`,
before deploying the API version that lists retry eligibility.
Before accepting STS-funded payments, commission the meter's `smart_meters` row:
set `credit_mode='sts'`, `sts_meter_number` to its registered DRN/PAN or IDRecord,
`sts_installation` to the provisioned provider installation name, and `sts_currency`
to that installation's three-letter currency. All are required for STS mode.

Each energy credit snapshots the delivery mode when issued. Payment processing
locks that configuration for the transaction. STS-mode payments create an
`energy_credit` and update the balance, but create no credit or reconnect command.
Vending requires a confirmed payment with an STS snapshot,
the configured meter/installation, matching currency and exact payment amount.
Only hundredth-based currency tariffs (`minor_units_per_major=100`) are accepted.
Amounts represent credit purchases; fees must already be excluded from the recorded
payment. Vending an unrelated amount or an unlinked purchase is rejected.

The payment UUID is both the unique funding identity and provider `messageId`.
A second reference for that payment is forbidden. The database also rejects
kWh credit commands for STS credits/meters. Switching an automatically credited
meter to STS later does not make its old payments vendable. Earlier unlinked
reservations remain visible for reconciliation; new ones require funding.

The existing kWh ledger is accounting based on the platform tariff; actual provider
units/rounding are retained in the vend result. Automatic dispatch and accounting
adjustment to provider units remain separate work. Do not independently vend
outside this system for a payment already delivered through another route.

## Vend a funded payment

From `services/api-go`, set `DATABASE_URL`, `PRISMVEND_URL`, and the stable
`PRISMVEND_INSTALLATION`. Use the confirmed payment UUID:

```sh
go run ./cmd/sts-vend -payment PAYMENT_UUID -meter 12345678901 -amount-minor 5001
```

The amount is currency hundredths, never kWh. The command checks funding before
calling the provider. A successful repeated request prints saved tokens without
contacting PrismVend. Deliver every returned token in order, including key-change
tokens; restrict output to the intended operator/customer. `hWh` denotes 100 Wh.
This manual workflow does not acknowledge a hardware command or confirm physical
entry. Physical/provider commissioning is still required.

## Connection failures and active requests

`ErrVendInProgress` means another process owns this payment's active vend; wait
for it to finish and retry the same command. This status does not call for provider
reconciliation.

`ErrVendNotSent` means the HTTP client failed before obtaining a usable connection,
so no vending POST could have been sent. The database records a safely retryable
reservation. Repeat the same payment command when connectivity returns; its
funding is rechecked before each retry, and its canonical identity never changes.
Proven-unsent reservations remain visible in `-list-unresolved`, including when
a refund or configuration change blocks their next attempt. `safe_to_retry`
records transport evidence only; it is not permission to bypass funding checks.
HTTP errors after a connection was obtained, including a lost reply, remain
uncertain. This distinction uses Go's [HTTP connection trace](https://pkg.go.dev/net/http/httptrace#ClientTrace).
No HTTP status code alone is treated as proof of non-issuance.

## Preserve issued tokens if saving fails

If PrismVend returns tokens but the database save fails, the service returns
those tokens **with** the persistence error. The CLI prints the complete result
to stdout and exits nonzero, reporting the error on stderr. Capture stdout in a
restricted file so it can be imported through `-recover`; do not re-vend this
payment or assume a failed CLI exit means no tokens were issued.

```sh
umask 077
set -C # refuse to overwrite an existing recovery file
go run ./cmd/sts-vend -payment PAYMENT_UUID -meter 12345678901 -amount-minor 5001 > recovered.json
```

An empty result file from an earlier/pre-send failure contains no issued tokens;
check the error. Protect saved tokens and deliver each purchase only once.

## Reconcile uncertain outcomes

A crash or ambiguous transport/response failure leaves the payment unresolved;
it is never automatically resent. Inspect the queue without contacting PrismVend:

```sh
go run ./cmd/sts-vend -list-unresolved
```

The listing includes all incomplete reservations for the configured installation,
up to 200 oldest first. Its `status` identifies the next kind of work:

| Status | Meaning |
|---|---|
| `outcome_uncertain` | No definite result; inspect provider evidence before proceeding. An active vend may still be completing. |
| `retryable_unsent` | No request was sent and current funding checks pass; retry the same payment when ready. |
| `retry_blocked` | No request was sent, but current funding or configuration prevents retry. |
| `needs_balance_reversal` | Legacy non-issuance needs its missing compensating balance entry. |

Blocked rows include `retry_blocked_reason`, such as `payment_reversed`,
`installation_changed`, or `currency_changed`. This is computed from the same
eligibility function used by the funding trigger, so changes appear even before
a retry is attempted and the reason clears when eligibility is restored.
`last_error`, when present, is the previously recorded transport error, not the
current funding diagnosis. Listing never contacts PrismVend or changes a
reservation; a database read failure is returned as an error, not as a blocked
funding status. Completed vends and fully reconciled non-issuance are excluded.

Use the original reference to obtain provider transaction evidence and the complete
ordered token result through the provider's operator procedures. Then save the
recovered `STSVend` JSON (the same shape printed by a successful command) in a
restricted local file, including tokens, units/unit name, vend time and IDRecord:

```sh
go run ./cmd/sts-vend -reference PAYMENT_UUID -meter 12345678901 \
  -recover recovered.json -actor operator-name -evidence 'provider transaction/support reference'
```

Recovery validates the result and atomically saves it with the operator/evidence
audit. Retrieving the original funded payment then returns those saved tokens.
It never calls the provider. Confirm the recovered meter and purchase against the
reservation before import; evidence is an operator attestation, not API verification.

If the provider definitively confirms **no issuance**, close the uncertainty:

```sh
go run ./cmd/sts-vend -reference PAYMENT_UUID -meter 12345678901 \
  -not-issued -actor operator-name -evidence 'provider confirmation reference'
```

This records an audited terminal non-issuance and an exact compensating credit
entry, subtracting only that payment's kWh and money from its assignment balance.
The customer summary excludes reversed energy. Original payment/credit history
is retained. It does not resend tokens or perform a cash refund; arrange any
refund through the payment provider's procedure. Repeating the vend returns
`ErrVendNotIssued`. If the balance no longer holds the whole credit, the
non-issuance is still recorded. The reversal uses the largest portion of that
credit supported by both remaining balances, capped at the original credit.
It rounds the monetary portion down to whole minor units, derives the kWh
portion from that same proportion of the original credit, and rounds kWh down
to six decimal places. If either portion rounds to zero, neither is subtracted.
Legacy credits with zero or absent monetary value reverse only available kWh.
Neither balance goes negative; every unreversed amount, including rounding
remainders, is recorded as a shortfall on the reversal row.

For example, a credit of 0.200040 kWh worth 5,001 minor units with only 0.1 kWh
remaining reverses 0.100000 kWh and 2,500 minor units. It records shortfalls of
0.100040 kWh and 2,501 minor units, preserving the remaining monetary balance.
The paired reversal does not repair pre-existing balance drift or establish a
single tariff for an aggregate balance. Existing reversal entries remain
historical records; this rule applies to new reconciliations. Shortfalls are
discrepancies to investigate, not customer debt. List them without contacting
PrismVend:

```sh
go run ./cmd/sts-vend -list-shortfalls
```

A resolved provider outcome cannot be overwritten. Reconciliation and vending
use an exclusive cross-process owner, so an operator cannot close a live
request before its provider exchange finishes.
Legacy unlinked reservations can also be resolved by their original reference.
Never delete a reservation or invent a new reference to retry uncertainty.

### Repair non-issuance recorded by an older version

Previously closed funded vends without a compensating entry appear in
`-list-unresolved` with `needs_balance_reversal: true`. Repeat `-not-issued` with
the original provider evidence to apply the missing reversal and new audit entry.
This preserves the original non-issuance timestamp and cannot compensate twice.
Legacy **unfunded** reservations lack a provable platform credit link; closing
those does not alter an account balance.
