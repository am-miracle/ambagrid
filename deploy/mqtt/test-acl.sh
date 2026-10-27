#!/bin/sh
# Disposable TLS broker exercising the real production ACL/config. No customer
# credentials or keys are used; all generated files live in a temporary directory.
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
fixture_dir=$(mktemp -d "${TMPDIR:-/tmp}/ambagrid-acl.XXXXXX")
container_name="ambagrid-acl-test-$$"
cleanup() { docker rm -f "$container_name" >/dev/null 2>&1 || true; rm -rf "$fixture_dir"; }
trap cleanup EXIT HUP INT TERM
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$fixture_dir/server.key" -out "$fixture_dir/server.crt" -days 1 -subj '/CN=localhost' -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' >/dev/null 2>&1
chmod 755 "$fixture_dir"
# Test fixtures only: production keys/passwords must use restrictive ownership.
chmod 644 "$fixture_dir/server.key" "$fixture_dir/server.crt"
touch "$fixture_dir/passwords"
chmod 600 "$fixture_dir/passwords"
docker run --rm -v "$fixture_dir:/fixtures" --entrypoint sh eclipse-mosquitto:2.0 -c '
 set -e
 for username in gateway-ng-kaji-01 device-ng-kaji-01-meter-01 telemetry-ng-kaji-01 gateway-other-site device-other-site-meter-01; do
  mosquitto_passwd -b /fixtures/passwords "$username" acl-test-password
 done
 chmod 600 /fixtures/passwords /fixtures/server.key
 chown 1883:1883 /fixtures/passwords /fixtures/server.key
'
docker run -d --name "$container_name" -p 127.0.0.1::8883 -v "$repo_dir/deploy/mqtt/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro" -v "$repo_dir/deploy/mqtt/commands.acl:/mosquitto/config/commands.acl:ro" -v "$fixture_dir:/mosquitto/secrets:ro" eclipse-mosquitto:2.0 >/dev/null
port=$(docker port "$container_name" 8883/tcp | sed 's/.*://')
attempt=0
until docker logs "$container_name" 2>&1 | grep -q 'running'; do
 attempt=$((attempt+1)); if [ "$attempt" -ge 20 ]; then docker logs "$container_name"; exit 1; fi
 sleep 0.2
done
cd "$repo_dir/services/edge-agent-go"
MQTT_ACL_TEST_BROKER="ssl://127.0.0.1:$port" MQTT_ACL_TEST_CA="$fixture_dir/server.crt" go test -tags=integration ./internal/integration -run TestCommandBrokerAccessControl -count=1 -v
