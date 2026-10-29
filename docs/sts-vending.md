# STS vending with PrismVend

The platform integrates PrismVend's VendCredit2 API, documented in the
[official API reference, revision 5.22.2](https://stsweb.prismcrypto.co.za/docs/PrismVend/PrismVend%20Web%20Vending%20API.pdf)
(sections 3.3, 4.4, 4.6 and 5.4.5–6). Provision the
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

| Result | Action |
| --- | --- |
| `ErrVendInProgress` | Another process owns the vend. Wait, then retry the same command. |
| `ErrVendNotSent` | No usable connection was obtained. Retry the same payment when connectivity returns. |
| Uncertain outcome | Obtain provider evidence and reconcile; never automatically resend. |

Retries keep the original payment identity and recheck funding. Unsent requests
remain in `-list-unresolved` even when a refund or configuration change blocks
retry. `safe_to_retry` describes transport evidence, not funding eligibility.
Once a connection was obtained, a lost reply or HTTP error remains uncertain;
no HTTP status alone proves that tokens were not issued.

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

`retry_blocked_reason` reflects current funding/configuration checks, for example
`payment_reversed`, `installation_changed`, or `currency_changed`. `last_error`
is the previous transport error. The listing excludes completed cases, never
contacts PrismVend or changes a reservation, and returns database failures as errors.

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
Existing reversal entries remain unchanged; this rule applies to new
reconciliations. It does not repair earlier balance drift. Investigate shortfalls
as accounting discrepancies, not customer debt:

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

## Provider protocol reference

The integration uses these PrismVend contract details:

- POST form fields to `/stsvend/VendCredit2.ini`: `meterId`, `subclass=0`,
  `value` in currency hundredths, and `messageId`. Optional `vendType` defaults to 1.
- Registered meters accept DRN11 or PAN16/17/18; blind vending accepts IDRecord35.
- `messageId` allows ASCII letters/digits and `_ - . ,`, up to 40 characters.
  Reusing it prevents another vend but returns a duplicate error (illustrated
  as HTTP 500), rather than the original tokens.
- Preserve ordered `tokenDec_1`…`tokenDec_N` (20 digits each) and `numTokens`;
  key-change tokens may accompany credit. Other result fields include
  `unitsActual`, `unitName`, `vendTimeUnix`, `idRecord`, and `toIdRecord`.
- `QueryTx` counts remaining module transactions; it is not a transaction-result
  lookup. Recover uncertain outcomes through the operator workflow above.

Protocol tests use a local HTTP server. A live PrismVend installation, licensed
Security Module, production keys, tariffs and physical token acceptance require
commissioning.
