# Command Ledger

The edge ledger records meter commands so retries and restarts do not apply
credit twice. If a device may have acted but its reply is lost, the command
stays unresolved until it can be retried safely or reconciled.

## Decision

`adapter.Ledger` wraps a `CommandAdapter` and stores each command ID in SQLite
(`command_ledger`, schema version 11).

### Intent is recorded before sending

Each adapter calls `adapter.BeforeSend(ctx)` once, after acquiring a ready
transport and immediately before sending. The hook commits an `in_flight` row;
if that fails, nothing is sent. A timeout while waiting for the transport
records no intent, so the command can be retried.

Conformance tests check this contract. If an adapter reports success without
calling the hook, the ledger saves the applied result and returns
`ErrSendHookSkipped`. A failure without the hook is treated as unsent.

### States

```text
           ┌──────────── device ack ────────────► applied   (terminal)
in_flight ─┼──────────── device rejects ────────► rejected  (terminal)
           └── timeout / transport error / crash ► unknown
```

- **applied**: a retry returns the stored result without contacting the device.
- **rejected**: the device definitely did not apply it. A retry returns the same
  rejection; trying again needs a new command ID, matching the upstream
  `failed` state.
- **unknown**: the command may have reached the device but no reply confirmed the
  outcome. On startup every `in_flight` row is marked `unknown`, since no
  process can still be waiting on its reply.
- A failure before `BeforeSend` (unsupported, invalid, cancelled while queued)
  writes no row, so a retry runs normally.

Outcomes are recorded with `context.WithoutCancel`: once the device may have
acted, the result is written even if the caller has given up. If that write
itself fails, the row stays `in_flight` and is treated as unknown.

Reusing a command ID with different parameters returns `ErrInvalidCommand`.
The ledger stores a SHA-256 of the command to detect this.

### Retrying an unknown outcome

| Command or device | Action |
|---|---|
| Disconnect, reconnect, status | Resend: they set or read absolute state. |
| Credit by STS token | Resend: meters refuse a token they already accepted. |
| Device declares `idempotent_refs` | Resend with the same reference. |
| Adapter implements `adapter.Reconciler` | Ask the device; mark applied, or resend if it confirms the command was not applied. |
| kWh credit, none of the above | Return `ErrOutcomeUnknown` and never resend automatically. |

An unknown credit that cannot be retried safely needs
[reconciliation](revenue-protection.md#reconciliation).

## Retention

A periodic job deletes `applied` and `rejected` rows older than
`EDGE_COMMAND_RETENTION` (default 30 days), in batches of 500 every
`EDGE_COMMAND_PRUNE_INTERVAL` (default 1 hour). `in_flight` and `unknown` rows
are never pruned: deleting one would silently turn "unknown" into "never
happened".

Keep retention longer than the upstream retry window, including
`API_METER_COMMAND_TTL` (default 24 hours). The edge flags commands that could
outlive retention but still executes them; see Operations below.

## Operations

- `ambagrid_edge_commands_unresolved` and
  `ambagrid_edge_commands_oldest_unresolved_seconds` report commands in
  `in_flight` or `unknown`; `/health` reports `unresolved_commands`. A value
  above zero that persists means a customer's credit may not have arrived.
- `ambagrid_edge_command_expiry_beyond_retention` at 1 means a pruned ledger
  entry could let a command run twice; lower `API_METER_COMMAND_TTL` or raise
  `EDGE_COMMAND_RETENTION`. It reflects the last nonempty poll; empty polls
  leave it unchanged. The edge logs an error when the flag changes from 0 to 1.
- Unresolved commands do not fail the health check: they need reconciliation,
  not a gateway restart.
- `ErrTimeout` from the ledger is not a device protocol timeout; count those at
  the transport exchange.

## Rollback

Schema version 11 adds fractional-millisecond request timestamps and updates
its relay-ordering index. An older binary refuses to open a newer database;
rolling back requires restoring a database backup from that binary's schema
version.

## Delivery

The edge polls api-go over outbound HTTPS and reports outcomes using its site
bearer key (`API_EDGE_API_KEYS` on the platform, `EDGE_COMMANDS_API_KEY` on the
edge). See [the API reference](api.md#post-v1edgecommandspoll) for the contract.

Polls prioritize `requested` and `queued` commands over `sent`, oldest first
within each group. The edge executes the selected batch oldest first.

```text
api-go                                 edge agent
POST /v1/edge/commands/poll  ───────►  runner, one command at a time, oldest first
  expire requested, mark queued          expired?          → refuse, report expired
                                         older relay cmd?  → refuse, report failed
                                         otherwise         → ledger.Execute
POST /v1/edge/commands/{id}/report ◄──  command_reports outbox
  status + audit event                   written in the ledger transaction
```

### Reports share the ledger's transaction

The SQLite store writes each report row in the same transaction as the ledger
change it describes: `sent` when the intent is recorded, `acknowledged` when
the device confirms, `failed` when it rejects or the edge refuses the command,
`expired` when the edge refuses a command past its deadline. An unknown
outcome writes no report, so the platform keeps the command at `sent`, the
state its reconciliation view looks for.

Reports are delivered in order per command, using local transition time for
scheduling so device clock skew cannot delay them. Transient failures retry
with backoff from 5 seconds to 10 minutes. Responses `400`, `404` and `409`
abandon the report for reconciliation and increment
`ambagrid_edge_command_reports_abandoned`; `401` and `403` retry until credentials
are fixed. Failed reports truncate messages to 500 UTF-8 bytes, marked `…`;
`last_error` retains the full text.

### Expiry

`meter_commands.expires_at` defaults to 24 hours after the request
(`API_METER_COMMAND_TTL`). When the site polls and in a background sweep, the
platform expires overdue `requested` commands: those have never been delivered.
The sweep runs at startup and every minute, settling at most 200 commands per
batch across all sites, with audit and outcome publication in one transaction.
The edge checks the remaining lifetime before sending and refuses a late
command it has never sent; see Gateway clock below. A command that may already have reached the device
is never reported as expired, nor resent after its deadline.

A `queued` command was delivered, so the edge may have sent it before the
deadline even if its report arrives later. The platform never expires it.
Polls keep returning it past its deadline until the edge settles it, as
`expired` if it never sent it or with the device's outcome if it did. A
delivery whose poll response was lost settles the same way. Platform expiry is
therefore final: a later report other than `expired` is retained as conflicting
evidence without changing status or publishing another outcome. After durable
capture, `202` tells the edge that the report reached reconciliation; a storage
failure leaves it retryable. This handles rows expired incorrectly by older
platform code without silently contradicting previously published failures.

### Reconciliation and rollout

Use the [reconciliation endpoint](api.md#get-v1meter-commandsreconciliation)
to find conflicting reports, overturned expiries and overdue unknown outcomes.
The API reference defines its filters, reason codes and response fields.

Every expiry audit event now records `previous_status`. An expiry without it
was written by pre-fix code, which also expired `queued` commands that may
have been sent; that marker, not a deployment timestamp, identifies legacy
expiries. Migration `202610270002_command_reconciliation_indexes` indexes the
conflict and legacy-expiry audit events the listing starts from. The listing is
correct without it, only slower, so the engine migration and the API can
deploy in either order.

Deploy this API version before updating edge agents; edge agents need no
change. Verify capture with a known conflicting report: expect `202`,
unchanged platform status, one conflict audit event, and no new outcome event.
Do not reopen expired rows or resend hardware commands as a rollout repair.

Inventory every legacy expiry with its current state. Rows that have left
`expired` already appear in the listing as `overturned_expiry`. Rows still
`expired` may have been delivered or applied before the deadline. Their prior
delivery state cannot be recovered from the platform alone:

```sql
SELECT a.site_id, mc.command_id, mc.meter_id, mc.command_type, mc.status,
       mc.requested_at, mc.expires_at, a.occurred_at AS legacy_expired_at,
       mc.sent_at, mc.acknowledged_at, mc.failure_reason
FROM audit_events a
JOIN meter_commands mc ON mc.command_id::text = a.subject_id
WHERE a.subject_type = 'meter_command' AND a.action = 'meter_command.expired'
  AND a.actor_id = 'system:command-expiry' AND NOT (a.metadata ? 'previous_status')
ORDER BY (mc.status = 'expired'), a.occurred_at, mc.command_id;
```

Compare these rows with edge ledger results and provider/device evidence. Reports
already abandoned after a `409` remain in the edge's SQLite `command_reports`
table until pruning; preserve those records before retention removes them and
submit verified evidence through the report endpoint. It durably accepts the
conflict for review. This read-only workflow does not reverse previous failure
events, resolve conflicting outcomes, or authorize a replacement command.

### Relay ordering

Before sending a disconnect or reconnect, the edge finds the newest relay
command for that meter that was or may have been sent, ordered by
`requested_at` then command ID. If that command is newer, the older one is
refused as `failed: superseded by newer relay command <id>`. Commands within
one poll run oldest first, so a disconnect and a later reconnect both apply, in
order. SQLite preserves the full request timestamp, including nanoseconds, so
ordering survives restarts. Version 10 rows have already lost their fractional
millisecond; after migration they conservatively supersede other commands in
that same millisecond, even if a later command might otherwise have run.
Ledger pruning can remove old entries, but expiry stops commands that
old from arriving, so the newest surviving entry still decides.

### Credit

`smart_meters.credit_mode` says where a meter's prepaid balance lives:

- `platform` (the default) meters are relay-controlled against the balance
  AmbaGrid holds. A payment reconnects them on an empty-to-positive transition.
- `meter` meters keep their own balance. Every payment sends a `credit_meter`
  command for the kWh it granted, linked by `credit_id` to the energy credit
  that funded it; the meter reconnects itself once it holds credit.
- `sts` meters use only funded STS vending: payments create no automatic credit
  or reconnect. Delivery mode is immutable on the credit; vending and kWh
  commands cannot deliver the same funded credit. See [STS vending](sts-vending.md).

`meter_commands.credit_kwh` carries the amount, required exactly for credit
commands, and one energy credit can be delivered by at most one command. A
lost credit reply stays unknown unless the device declares `idempotent_refs`
or `outcome_query`, in line with the retry table above.

### Outcome events

Settled outcomes are published through the outbox, in the transaction that
records them: `meter.command.acknowledged`, and `meter.command.failed` for both
`failed` and `expired`, as one protobuf message, `MeterCommandOutcome`
(`proto/meter_commands.proto`). Platform expiry is reported by
`system:command-expiry`, edge outcomes by `edge:<site_id>`. `sent` and unknown
outcomes publish nothing. `meter.command.requested` also carries `expires_at`
and the credit amount.

### Gateway clock

Expiry does not trust the gateway wall clock. The poll response gives each
command's remaining time, `expires_in_ms`, measured on the platform clock; the
edge adds it to the moment it sent the poll on Go's monotonic clock, which wall
clock changes do not move. Report times are corrected by the offset between the
gateway clock and the platform's HTTP `Date` header, ignored below 2 seconds
because the header has one-second resolution. The platform also clamps every
reported time to between the command's request and its own now, so a badly
wrong gateway cannot record impossible times.

### Command adapters

Each configured command route uses the durable ledger before reaching the device.
See [per-device configuration](hardware-adapters.md#per-device-configuration)
for adapter selection and [the MQTT command protocol](hardware-adapters.md#mqtt-command-protocol)
for device integration. Production MQTT commands require
[broker authentication, TLS and topic ACLs](mqtt-command-security.md).
