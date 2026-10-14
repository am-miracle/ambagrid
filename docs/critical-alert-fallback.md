# Critical Alert Fallback Design

When packet data is unavailable, the edge agent may send a small set of urgent
alerts by SMS. SMS is a secondary delivery path for alerts only. It never
carries routine telemetry, commands, queue health, heartbeats, or recovery
messages.

## Critical event codes

Only these codes qualify in version 1:

| Code | Scope | Qualification |
|---|---|---|
| `BATTERY_OVERHEAT` | Battery asset | The configured critical battery temperature rule opens an incident. |
| `INVERTER_FAILURE` | Inverter asset | The inverter reports a hard fault or the gateway confirms inverter failure after local debounce. |
| `TAMPER_DETECTED` | Affected asset | A trusted hardware tamper signal remains active after local debounce. |
| `SITE_OUTAGE` | Site (`asset=site`) | The gateway confirms that the whole site has lost supply after local debounce. |

Warnings, low battery, individual stale devices, queue growth, gateway
offline/online heartbeats, payments, and meter commands do not qualify. Adding
a code requires an explicit policy change; provider configuration cannot turn
an arbitrary event into an SMS alert.

One fallback incident covers one continuous condition. Repeated readings while
the condition remains active do not create more messages. Recovery closes the
local incident, allowing a later recurrence to use a new event identity.

## Message contract

Version 1 uses ordered `key=value` fields and GSM-7 characters:

```text
AMBAGRID|v=1|site=ng-kaji-01|asset=batt-01|seq=18422|code=BATTERY_OVERHEAT|temp=68.2|ts=1730000000
```

Required fields:

- `v`: contract version, currently `1`
- `site`: provisioned site ID
- `asset`: provisioned asset ID, or the reserved value `site` for `SITE_OUTAGE`
- `seq`: durable site-local edge sequence of the first event in the incident
- `code`: one of the four allowed codes
- `ts`: original event time as UTC Unix seconds

Code-specific fields stay small: `temp` is required for
`BATTERY_OVERHEAT`; other version 1 codes need no extra field. IDs and values
must not contain `|` or `=`. Unknown versions, duplicate keys, unknown codes,
invalid numbers, and messages longer than one SMS segment are rejected.

`seq` extends the earlier example because timestamps alone are not safe
idempotency keys. Retries reuse the exact same message and sequence.

## Send decision

The edge agent persists the event before attempting either delivery path. It
sends SMS only when both conditions are true:

1. The event opened one of the four critical fallback incidents.
2. Normal upload for that event has failed 3 consecutive times, or no upload
   has succeeded for 2 minutes.

Both thresholds are deployment configuration. The defaults avoid switching to
SMS for a brief packet-data interruption while still escalating urgent events
quickly. Normal upload and eventual replay continue after an SMS attempt;
acceptance by the modem does not acknowledge the queued event.

Fallback state and attempt counts must be durable across edge-agent restarts.
For one incident, the agent makes at most 3 SMS attempts over 15 minutes. It
also enforces a default site budget of 6 attempts per hour and 20 per day.
Rate-limited incidents remain in the normal replay queue and are reported in
local health metrics.

## Server idempotency

The canonical event identity is `edge:<site>:<seq>`. The SMS receiver derives
this key from the message. Normal replay must derive the same key from the edge
envelope rather than from Kafka partition and offset.

The server handles both arrival orders:

- SMS first: create the alert with the compact SMS details; replay later stores
  the full reading, marks the fallback event as replayed, and reuses the alert.
- Replay first: record the SMS receipt against the existing alert without
  opening another one.
- Provider webhook retry or edge SMS retry: record the delivery attempt, then
  return the existing result for the canonical event identity.

The provider message ID is useful for webhook deduplication, but it is not the
event identity because each retry can receive a different provider ID.

## Gateway integration

The configured destination is a platform-controlled inbound SMS number, not a
personal operator number. The delivery flow is:

```text
edge agent -> local modem/SIM -> mobile carrier -> inbound SMS provider
           -> signed provider webhook -> SMS receiver -> alert ingestion
```

The edge-side adapter has one operation: send a destination number and message
body through the local modem. It returns accepted or failed; there is no SMS
command channel and the edge agent does not wait for an application reply.

The server-side provider adapter must expose the sender number, recipient
number, message body, provider message ID, and receive time, and must support
webhook signature verification. The sender number is mapped to a provisioned
gateway and site; a message claiming another site is rejected.

The normalized receiver endpoint is `POST /v1/sms/inbound`. Its JSON fields are
`message_id`, `from`, `to`, `body`, and `received_at`; `X-SMS-Signature` is a
hex HMAC-SHA256 over the raw body. The endpoint is owned by `api-go`; enable it
with `API_SMS_ENABLED`, set `API_SMS_WEBHOOK_SECRET`, and map provisioned
senders with `API_SMS_SENDERS`, for example
`+2348000000000=ng-kaji-01:gateway-01`.
Cost accounting uses `API_SMS_OUTBOUND_COST_MINOR`,
`API_SMS_INBOUND_COST_MINOR`, `API_SMS_NUMBER_RENTAL_MINOR`,
`API_SMS_MONTHLY_BUDGET_MINOR`, and `API_SMS_CURRENCY`. Values are in the
currency's minor unit. Each unique provider receipt records its configured
cost, and the receiver logs a warning when monthly usage reaches 80% of budget.

Provider selection is deployment-specific because inbound-number coverage and
pricing vary by country. A provider is eligible only if it offers an inbound
number in the deployment country, signed webhooks, documented throughput, and
delivery records. For a Nigerian pilot, the reference candidate is an
Africa's Talking shared short code; its documentation lists Nigerian SMS short
codes and a 24–48 hour shared-code onboarding path:
[Africa's Talking Nigeria onboarding](https://help.africastalking.com/en/articles/2285287-how-long-does-it-take-to-get-onboarded-in-nigeria).
Procurement must confirm webhook signing, the current rate card, and carrier
coverage before enabling it. Twilio is not a suitable Nigerian inbound
provider today because its Nigeria guidance says two-way SMS is unsupported:
[Twilio Nigeria SMS guidelines](https://www.twilio.com/en-us/guidelines/ng/sms).

## Rate and cost controls

Every deployment records these values before enabling fallback:

- provider and carrier name
- inbound number and gateway SIM sender number
- provider webhook throughput limit
- modem send-rate limit
- edge-SIM cost per outbound segment
- provider cost per inbound segment and monthly number rental
- currency and monthly fallback budget

Costs are configuration and operations data rather than source-code constants.
The cost of one fallback attempt is one edge-SIM outbound segment plus one
provider inbound segment at the deployment's contracted rates. Those two unit
prices are required configuration because they vary by carrier and contract.
The receiver records one billable segment per accepted version 1 message and
raises an operational warning at 80% of the monthly budget. Crossing the
budget does not silently expand the critical list; any hard cutoff is an
operator-owned deployment policy.

## Observability and boundaries

Record the incident identity, decision reason, attempts, modem result,
provider receipt, parse result, deduplication result, and linked alert ID. Do
not put secrets or full phone numbers in logs.

This design does not include two-way SMS, remote commands, acknowledgements to
the edge agent, routine telemetry transport, operator chat, or automatic
provider failover.
