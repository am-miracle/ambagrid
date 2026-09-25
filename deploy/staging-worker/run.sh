#!/usr/bin/env bash
# If any process exits, stop the rest and exit non-zero so Render restarts the
# whole worker instead of leaving it half-running.
set -euo pipefail

/app/engine-rust migrate
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q -f /app/seed.sql

pids=()
stop_all() {
  kill -TERM "${pids[@]}" 2>/dev/null || true
  wait "${pids[@]}" 2>/dev/null || true
}
trap 'stop_all; exit 143' TERM INT

/app/engine-rust serve & pids+=($!)
/app/engine-rust publish-outbox & pids+=($!)
/app/ingestion-go & pids+=($!)
if [[ "${SIMULATOR_ENABLED:-true}" == "true" ]]; then
  /app/virtual-meter & pids+=($!)
fi

set +e
wait -n "${pids[@]}"
status=$?
set -e

echo "staging worker: a process exited with status $status; stopping the rest" >&2
stop_all
exit $(( status == 0 ? 1 : status ))
