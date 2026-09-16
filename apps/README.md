# AmbaGrid control room

The control room reads the operator API from `http://localhost:8081` by
default. Set `VITE_API_BASE_URL` to use another API origin.

Live telemetry currently uses the polling adapter in
`src/lib/telemetry-source.ts`. React callers depend on the `TelemetrySource`
interface and receive complete site snapshots; timers, pagination, request
cancellation, and API wire-format mapping stay inside the adapter.
