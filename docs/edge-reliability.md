# Edge Reliability

Rural mini-grid sites often run on weak or intermittent cellular networks. AmbaGrid must treat offline operation as normal, not exceptional.

The edge reliability layer should let a site keep collecting telemetry, recording critical events, and replaying data when connectivity returns.

## Edge Agent

The edge agent is the site-side process that runs near the equipment.

It may run on:

- Raspberry Pi
- industrial Linux gateway
- inverter-side controller
- local mini PC
- operator-managed site server

Responsibilities:

- collect local telemetry
- normalize local device events
- buffer events during outages
- forward events upstream when online
- expose local health
- send critical fallback alerts

## Store-and-Forward Buffering

The edge agent should write events to local storage before forwarding them upstream.

Required behavior:

- preserve event order per device where possible
- avoid data loss on process restart
- cap disk usage
- expose queue depth
- expose oldest pending event age
- mark replayed events clearly
- stop accepting non-critical data before corrupting local storage

Suggested event state:

```text
received
  -> persisted
  -> pending_upload
  -> uploaded
  -> acknowledged
  -> expired
```

## Replay After Recovery

When the network returns, the edge agent should replay buffered events without hiding that they are delayed.

Upstream records should include:

- original event timestamp
- edge receive timestamp
- upload timestamp
- gateway ID
- replay flag
- sequence number where available

This keeps analytics honest. A replayed voltage reading from two hours ago should not look like a fresh real-time reading.

## Offline Health State

The dashboard should show whether a site is live, delayed, or offline.

Useful indicators:

- last gateway heartbeat
- last telemetry upload
- pending queue depth
- oldest pending event age
- last successful sync
- last failed sync reason

## SMS Fallback

SMS should carry critical alerts only. It should not be used for routine telemetry.

Example shape:

```text
AMBAGRID|v=1|site=ng-kaji-01|asset=batt-01|code=BATTERY_OVERHEAT|temp=68.2|ts=1730000000
```

Use cases:

- battery overheat
- inverter offline
- full-site outage
- tamper alarm
- gateway online/offline heartbeat when data service is unavailable

SMS messages should be:

- short
- versioned
- structured
- idempotent where possible
- tied to known site and asset IDs

## OTA Updates

Over-the-air updates are important, but risky. Bad OTA can brick equipment or cut off a site.

OTA should not be built until device identity, signing, and rollback are understood.

Minimum requirements:

- signed update packages
- gateway identity
- staged rollout
- rollback plan
- version reporting
- update audit trail
- bandwidth-aware downloads
- operator approval for risky changes

Configuration updates should come before firmware updates.

## First Build Target

The first edge reliability prototype should prove this:

```text
Network goes down
  -> edge agent keeps writing telemetry to disk
  -> queue depth increases
  -> network returns
  -> edge agent replays events
  -> upstream records show original timestamps and replay status
```
