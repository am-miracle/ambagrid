# Production command broker access control

Authentication, encryption and topic authorization are preconditions for enabling
MQTT hardware commands. Adapter conformance alone does not establish them. The
[official Mosquitto configuration reference](https://mosquitto.org/man/mosquitto-conf-5.html)
defines authenticated user ACLs and deny-by-default behavior when `acl_file` is set.
The included configuration targets the pinned Mosquitto 2.0 image.

`deploy/mqtt/compose.yml` runs a dedicated TLS command broker on 8883 with anonymous
access disabled. It does not inherit the anonymous development broker. The main
Compose broker is for local development; its published ports bind to loopback.

Before starting the production broker:

1. Replace the example users/topics in `deploy/mqtt/commands.acl` with the exact
   commissioned identities and site prefixes. Remove the example second tenant.
2. Provision `deploy/mqtt/secrets/server.crt` and `server.key`. The certificate
   must cover the broker's real DNS name and include the intermediate chain.
3. Create `deploy/mqtt/secrets/passwords` with `mosquitto_passwd`. Use separate,
   strong credentials for each gateway and each device. Do not share credentials
   across sites or combine a telemetry publisher with a command identity.
4. Ensure the container's Mosquitto user (UID/GID 1883 in this image) can read the
   mounted secrets; restrict the directory and private files to that user/group.
   These files are gitignored. Back them up through the deployment's secret store.
5. Start with `docker compose -f deploy/mqtt/compose.yml up -d`.

For example, `mosquitto_passwd -c /path/to/passwords gateway-ng-kaji-01` creates
the file and prompts for the first password; omit `-c` when adding each device.
Never use `-c` on an existing production file, because it overwrites other users.

The ACL grants each gateway write access only to its site's command requests and
read access only to that site's replies. Each device reads its own command topic
and writes its own reply topic. Everything else is denied, including forged
replies, sibling-device topics, cross-site commands, and unrelated telemetry users.
Authorization uses authenticated **username**, not the caller-controlled client ID.
Do not replace the explicit users with global pattern permissions that would
also apply to unrelated users.

Configure the edge with `EDGE_MQTT_BROKER=ssl://broker.example:8883` (or another
TLS scheme such as `mqtts://`),
`EDGE_MQTT_USERNAME`, `EDGE_MQTT_PASSWORD`, and `EDGE_MQTT_CA_FILE`. The edge
refuses plaintext/anonymous command connections by default. The explicit
`EDGE_MQTT_ALLOW_INSECURE=true` escape hatch is for isolated development only.
The client cannot prove a remote broker's ACL is correct; commissioning must test
that deployed policy. Configure device credentials and CA verification similarly.

Run the real-broker regression check from the repository root:

```sh
deploy/mqtt/test-acl.sh
```

It creates a disposable TLS broker using the actual config/ACL and synthetic
credentials, then checks authorized command/reply delivery, rejected anonymous
access, cross-site command denial, and blocked forged replies. It removes its
container and keys afterwards. Docker, OpenSSL, ripgrep, and Go are required.
Changing production ACLs requires repeating these checks against that deployment
with its own test identities. Rotate/revoke credentials using the broker's
operational procedure; ACL and credential files are not managed by the edge.
