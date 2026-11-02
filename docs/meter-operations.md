# Meter operations and credit resolution

The control room's **Meters** tab provides the reconciliation queue, resolved
cases, command lookup, device observations, and status/relay command templates.
Operator keys are entered for the current panel session and kept only in memory.
They are never embedded in the frontend build or saved to browser storage.

## Deploy and commission

1. Apply engine migrations through `202610310001_retire_case_write_off.sql` before
   deploying this API. It adds cases, linked delivery attempts, refund accounting,
   capability profiles, and device observations. Existing credits and commands
   retain their identifiers and original outcomes.
2. Set `API_OPERATOR_KEYS=operator-name:secret-key,another-operator:another-key`
   on the API. These keys grant trusted operators fleet-wide meter operations;
   use distinct keys per person and rotate/revoke them through configuration.
   Site edge keys cannot authorize these actions. Keep the existing deployment
   gateway protections on read endpoints. Operators cannot supply their audit
   identity in a request body.
3. Commission a capability profile from verified hardware inventory. Database
   administrators can provision it as follows, substituting the real meter ID
   and only enabling capabilities verified for that device:

   ```sql
   INSERT INTO meter_command_profiles(meter_id,relay_control,status_read,prepaid_credit)
   VALUES ('commissioned-meter-id',true,true,true)
   ON CONFLICT(meter_id) DO UPDATE SET
     relay_control=excluded.relay_control,status_read=excluded.status_read,
     prepaid_credit=excluded.prepaid_credit;
   ```

   An absent profile denies manual commands and redelivery. It does not change
   existing automatic payment delivery. Edge adapters independently enforce
   their own configured capabilities before sending anything.
4. Deploy the API before new edge agents. The agents now send `observation` on
   acknowledgements. SQLite schema 12 persists it in the ledger transaction;
   older queued reports with no observations remain valid. Rolling an edge
   agent back requires a database backup compatible with the older binary.
5. Load the [adapter and backlog alert rules](../deploy/monitoring/README.md) in your
   monitoring deployment, and set `API_METRICS_ADDR` for the backlog rules. Repository files alone do not activate alerts.

## Reconciliation workflow

A case with no explicit record is **open**. Assigning investigation records
**investigating**, an owner, reason, and evidence. Device command status is a
separate lifecycle and is never rewritten by operator resolution.

`POST /v1/meter-commands/{command_id}/reconcile` uses an operator bearer key:

```json
{
  "owner": "operator-name",
  "finding": "not_delivered",
  "disposition": "redeliver",
  "reason": "Device rejected the credit",
  "evidence": "Device ledger inspected; the original reference was not applied"
}
```

Supported decisions:

| Disposition | Required finding | Effect |
| --- | --- | --- |
| `investigate` | `unknown` | Assign an owner; keep the case open for work. |
| `delivered` | `delivered` | Resolve using confirmed delivery evidence. Without a device acknowledgement, needs an investigation opened by another operator. |
| `redeliver` | `not_delivered` | Atomically create a linked attempt and resolve the original case. |
| `refund` | `not_delivered` | Record an external refund reference and reverse what the balance holds of its entitlement. |
| `review` | `unknown` | Record review of new evidence on a resolved case; preserve the original decision. |

Reason and evidence are required for every decision. Refund additionally requires
`refund_reference`, identifying an externally completed refund. **This endpoint
does not call a payment provider or send money.** It records the external action
and reverses the original kWh and monetary entitlement in one transaction. When the
pooled balance has already been consumed, it reverses the jointly affordable part,
as STS reversals do, and records the rest as a shortfall.
`GET /v1/meter-commands/refund-shortfalls?limit=50&after_credit_id=UUID` lists them;
a shortfall is a discrepancy to investigate, not customer debt.
Provider payout automation and reservations
before external payout are outside this workflow. Do not use it as a refund
initiation endpoint.

Redelivery is the default compensation workflow when non-delivery is confirmed.
Refund is available when redelivery is inappropriate and the external refund
has been verified under the business's refund policy. Evidence must establish
non-delivery; a missing acknowledgement alone is insufficient.

Uncertain delivery remains under investigation. A retained `sent` or `acknowledged`
report on any attempt, or a legacy expiry, blocks redelivery and refund and holds an
undispatched replacement. If neither compensation path is currently safe, keep the
case open with an owner and supporting evidence.

### Delivery and compensation safety

- A replacement shares the original credit ID and amount, references its previous
  attempt, creates no new energy credit, and does not increment the balance.
- The entitlement row serializes compensation decisions. A unique predecessor
  link prevents branching attempts. Only failed/expired, funded meter-credit
  commands can be replaced or refunded. The current meter assignment must still
  match for redelivery.
- A refund or replacement prevents another compensation on that historical
  attempt. A later failed replacement can itself be investigated and replaced;
  the complete attempt chain remains available.
- Positive delivery evidence, a confirmed delivered case, legacy ambiguous
  expiries, or a recorded refund blocks compensation. Queued/sent outcomes must
  be investigated rather than compensated. Safe same-ID retries remain the
  edge ledger's responsibility; this workflow does not bypass it.
- Repeating the same completed decision returns its existing case and replacement
  ID. Changing an already resolved decision returns `409`.
- Conflicting reports are retained. New conflicting evidence marks a resolved
  case `needs_review` and restores it to the default queue. Review records a
  separate audit event and clears that flag without changing compensation.
  Identical replays do not reopen a reviewed case.
- All case changes and command creation write audit events in their transaction.
  Replacement command publication uses the existing transactional outbox.

`GET /v1/meter-commands/reconciliation` requires an operator key and omits resolved cases unless new evidence
needs review. It includes `case`, `needs_review`, and credit ID/amount where
available, and retains its existing site filter and cursor pagination.
`GET /v1/meter-commands/reconciliation/resolved?limit=50&after_command_id=UUID`
returns resolved cases in command-ID order; use the last returned ID for the
next page. Command lookup is also available by UUID.

## Commands and device observations

`GET /v1/meter-command-templates` returns the fixed status, disconnect, and
reconnect templates. It deliberately offers no unfunded manual credit command.
A reconnect is refused while a platform-balance meter's assigned customer has no
balance, since it would supply unpaid energy; meter-balance meters enforce their
own balance, and an unassigned meter has no balance to check.
All new command creation uses `POST /v1/meter-commands` with operator authentication:

```json
{
  "request_id": "a9fa2302-940f-4e98-891b-a04e18804b01",
  "meter_id": "commissioned-meter-id",
  "command_type": "disconnect_meter",
  "reason": "Authorized maintenance",
  "confirm_meter_id": "commissioned-meter-id"
}
```

A request UUID identifies one immutable operation; reuse it after network errors.
Changing its meter, command, actor, or reason returns `409`. Relay actions require
an exact target-meter confirmation. Status queries do not. Existing expiry,
relay ordering, capability checks, ledger, and command reporting apply.

`GET /v1/meter-commands/{command_id}` returns the original `command`, optional
`case`, and `observation` with `observed_at` and `received_at`. An acknowledgement
can report `relay_closed`, `balance_kwh`, `tamper_detected`, `firmware_version`,
and `fault_code`. Missing fields mean unknown, including tamper; they do not
mean false or zero. The panel marks observations over five minutes old as stale.
These snapshots do not overwrite live telemetry or infer state from a command.
Changed evidence on a repeated acknowledgement is retained as a conflict.

## Firmware policy and fault-code declarations

For MQTT command adapters, extend the commissioned capability YAML with exact
verified firmware versions and model-specific fault descriptions:

```yaml
firmware_versions: ["1.2.3"]
fault_codes:
  E_METER_LOCKED:
    description: "Meter cover open"
    severity: critical
```

These are illustrative values, not a vendor compatibility claim. Verify them
against the actual device model and its protocol before commissioning.

When a firmware policy exists, each credit or relay mutation first performs a
read-only status probe. Missing, rejected, or incompatible firmware prevents the
mutation before its send intent is recorded. Status queries remain available
for diagnosis. The probe does not consume the mutation's command ID and does
not claim that the mutation reached the device. The device must support the
existing MQTT status operation and report `firmware_version` in its reply.
Without a configured policy, existing device behavior is retained.

MQTT replies may also report `fault_code` on successful status reads. Rejection
`code` values are explained using the configured fault-code map while retaining
the raw code and vendor message in the failure reason. Unknown codes remain raw;
no vendor meaning is guessed. Firmware mismatch has a dedicated metric and
alert. Modbus remains telemetry-only; no firmware register or writable command
map is invented for hardware that has not been commissioned.


### Late delivery evidence and review

Polling, command reporting, and reconciliation decisions serialize per site.
An unsent replacement is withheld whenever any attempt for its entitlement has
acknowledged delivery or retained positive delivery evidence. Clearing a review
flag does not remove that delivery hold. Commands already delivered to an edge
remain in the normal outcome protocol because the platform cannot recall them.

Command lookup returns `latest_conflict`, including `report_id`, `conflict_count`,
reported status, device event time, receipt time, failure reason, and optional
observation. The panel shows this evidence alongside the original observation.
Review submissions must include `reviewed_conflict_id` and
`reviewed_conflict_count` from the displayed report; new evidence makes a stale
review return `409` until the operator reloads the case.

Write-off is retired: unknown delivery stays under investigation, and confirmed
non-delivery requires redelivery or a verified refund to settle the entitlement.
A consumed balance no longer blocks that refund; it records a shortfall.
Migration `202610310001_retire_case_write_off.sql` reopens existing write-offs as
investigations, preserving the previous decision in an audit event. Apply all
migrations through `202610310002_refund_shortfall.sql` before deploying this version, including the
preceding `202610300004_case_write_off.sql` migration if it was not already applied.
