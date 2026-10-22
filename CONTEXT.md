# AmbaGrid Operations

AmbaGrid models the operational state and actions of distributed mini-grids.

## Language

**Critical fallback event**:
A safety or site-availability event allowed to use the secondary alert path when normal data upload is unavailable.
_Avoid_: SMS telemetry, emergency telemetry

**Fallback incident**:
One continuous occurrence of a critical fallback condition, from its first qualifying event until recovery.
_Avoid_: SMS message, retry

**Event identity**:
The stable site and edge sequence that identifies one event across normal upload and fallback delivery.
_Avoid_: Timestamp key, provider message ID
