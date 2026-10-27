# Command Ledger

Status: accepted, 2026-10-24; extended 2026-10-25. Applies to the edge agent's meter commands and their delivery through api-go.

A meter command must reach the hardware at most once per command ID, even
across retries, lost replies, crashes, and power loss. A credit applied twice
gives energy away; a credit never applied leaves a paying customer
disconnected. When the edge cannot tell which happened, it says so instead of
guessing.

## The failure being solved

A command is sent to the meter, the meter applies it, and the reply never
arrives: the serial link drops, the gateway loses power, or the caller's
deadline expires. The edge has no record that the command succeeded, so a
retry sends it again.

A crash and a lost reply are the same failure. Recording the result after the
device replies cannot fix it, because the gap is between the device acting and
the edge learning that it acted.

## Decision

`adapter.Ledger` wraps any `CommandAdapter` and keeps a durable record per
command ID in the edge SQLite store (`command_ledger`, schema version 11).
Adapters only carry commands to the device; the ledger owns at-most-once
delivery.

### Intent is recorded before sending

Each adapter calls `adapter.BeforeSend(ctx)` immediately before transmitting,
after it holds the transport. The ledger commits an `in_flight` row inside
that hook. If the commit fails, the adapter aborts and nothing is sent.

The hook exists because only the adapter knows when transmission starts. A
command that times out while queued for the transport never reached the
device; recording it as unknown would strand every credit sent to an offline
meter. The conformance suite checks that every adapter calls the hook exactly
once, before the device sees anything, and never for a command that did not
leave the queue.

Transports call the hook too: the MQTT adapter passes it to `Publish`, which
runs it after its readiness check. The suite only covers adapters and
transports run through it, so the ledger also guards the contract itself. An
adapter that reports success without calling the hook has applied a command
the ledger never recorded; the ledger records it as applied, so a retry finds
it instead of sending it again, and returns `ErrSendHookSkipped`. A failure
without the hook is indistinguishable from a command that was never sent and is
still treated as one.

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
- **unknown**: the command reached the device but no reply confirmed the
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

An unknown credit fails closed. Resending risks a double credit that is hard
to recover; not resending risks a delayed credit, which the reconciliation
view "credit issued but meter not updated" in
[revenue protection](revenue-protection.md#reconciliation) already exists to
catch.

## Retention

A periodic job deletes `applied` and `rejected` rows older than
`EDGE_COMMAND_RETENTION` (default 30 days), in batches of 500 every
`EDGE_COMMAND_PRUNE_INTERVAL` (default 1 hour). `in_flight` and `unknown` rows
are never pruned: deleting one would silently turn "unknown" into "never
happened".

Retention must stay longer than the longest time upstream may retry one
command ID, so `API_METER_COMMAND_TTL` (default 24 hours) must be set well
below it. The two are configured in different services; the edge checks each
polled command's remaining lifetime against its retention. A non-empty poll
sets `ambagrid_edge_command_expiry_beyond_retention` to 1 if any command could
outlive retention, or 0 otherwise; empty polls leave it unchanged. The edge
logs an error only when the condition changes from false to true. The command
still runs.

## Operations

- `ambagrid_edge_commands_unresolved` and
  `ambagrid_edge_commands_oldest_unresolved_seconds` report commands in
  `in_flight` or `unknown`; `/health` reports `unresolved_commands`. A value
  above zero that persists means a customer's credit may not have arrived.
- `ambagrid_edge_command_expiry_beyond_retention` at 1 means a pruned ledger
  entry could let a command run twice; lower `API_METER_COMMAND_TTL` or raise
  `EDGE_COMMAND_RETENTION`. It reflects the last poll that returned commands.
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

Commands reach the edge by polling and outcomes return by report, both over
api-go with the site's bearer key (`API_EDGE_API_KEYS` on the platform,
`EDGE_COMMANDS_API_KEY` on the edge). Polling needs only outbound HTTPS, which
suits NAT and intermittent cellular links; api-go already owns
`meter_commands`, so it records each report and its audit event in one
Postgres transaction. Endpoints are described in [the API reference](api.md).

Each poll selects `requested` and `queued` commands before `sent` commands,
oldest first within each group. Sent commands fill any remaining batch capacity,
so unresolved outcomes cannot block later credits or relay commands. The edge
runs the selected batch oldest first.

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

A report waits while an earlier report for the same command is undelivered,
so the platform hears `sent` before `acknowledged`. Initial delivery is scheduled
from the edge's local transition time, independently of the device event time,
so a device clock ahead of the edge cannot delay its acknowledgement report.
Transient failures back off
from 5 seconds to 10 minutes. A report the platform refuses for good (`400`,
`404`, `409`) is kept as abandoned for reconciliation and counted in
`ambagrid_edge_command_reports_abandoned`; `401` and `403` keep retrying,
because a wrong key is an operator fix after which the reports still matter.

### Expiry

`meter_commands.expires_at` defaults to 24 hours after the request
(`API_METER_COMMAND_TTL`). When the site polls and in a background sweep, the
platform expires overdue `requested` commands: those have never been delivered.
The sweep runs at startup and every minute, settling at most 200 commands per
batch across all sites, with audit and outcome publication in one transaction.
The edge checks again before sending, against its own clock, and refuses a late
command it has never sent. A command that may already have reached the device
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

`GET /v1/edge/commands/reconciliation` uses the site's edge bearer key and lists
overdue `queued`/`sent` commands plus commands with conflicting reports. It
shows site health, contact and report receipt times, command age, overdue age,
and the latest conflict. All conflicting reports are retained in `audit_events`
as `meter_command.report_conflict`; exact retries are deduplicated. Commands at
sites that never return remain unresolved and visible instead of becoming
assumed failures. See [the endpoint contract](api.md#get-v1edgecommandsreconciliation).

Deploy this API version before updating edge agents. It needs no new database
migration. Verify capture with a known conflicting report: expect `202`,
unchanged platform status, one conflict audit event, and no new outcome event.
Do not reopen expired rows or resend hardware commands as a rollout repair.

Inventory pre-rollout expirations using the deployment timestamp as a cutoff;
their prior delivery state cannot reliably be recovered from status alone:

```sql
SELECT sm.site_id, mc.command_id, mc.meter_id, mc.requested_at,
       mc.expires_at, mc.sent_at
FROM meter_commands mc
JOIN smart_meters sm ON sm.meter_id = mc.meter_id
WHERE mc.status = 'expired' AND mc.requested_at < :'deployment_at'::timestamptz
ORDER BY mc.requested_at, mc.command_id;
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

[Broker authentication, TLS and topic ACLs](mqtt-command-security.md) are a
production precondition; the development broker is insufficient.

`EDGE_COMMAND_ADAPTER` selects the simulator or the MQTT adapter
(`internal/adapter/mqtt`), which speaks the protocol in
[hardware adapters](hardware-adapters.md#mqtt-command-protocol) and reads the
device's capability declaration from `EDGE_MQTT_CAPABILITIES_FILE`. A device
declaring `outcome_query` lets the ledger settle an unknown outcome by asking it.

## Not yet covered

- **Vendor drivers.** The MQTT adapter needs a device or bridge that speaks the
  AmbaGrid command protocol; the first vendor telemetry driver now reads Victron Cerbo GX battery registers
  over Modbus-TCP. Other vendor command drivers remain future work. See
  [vendor research](vendor-integration-research.md).
- **STS tokens.** The protocol and adapter accept a token, but the platform
  creates kWh credits on the payment path. A separate durable
  [PrismVend manual vending command](sts-vending.md) accepts explicit currency
  purchases and saves ordered tokens. Automatic payment-to-token delivery is
  not yet connected.
