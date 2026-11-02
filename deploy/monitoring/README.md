# Edge adapter alerts

Load `edge-alerts.yml` through the existing Prometheus server's `rule_files`
and scrape each gateway's `/metrics` endpoint (default port 2112). Keep an
`instance` and `site_id` label per gateway. Route critical alerts to the
on-call operator through your Alertmanager configuration.

These rules use the edge's actual metric names. They do not start a monitoring
server or expose gateway ports publicly. Choose scrape networking appropriate
to your gateway deployment. Silence rules for intentionally decommissioned
hardware; the telemetry rule intentionally detects devices that have never
completed a read. Command activity is event driven, so there is no idle-command
alert. Error counters reset on restart and `increase` handles that reset.

Validate rules with `promtool check rules deploy/monitoring/edge-alerts.yml`.
Run behavior tests with `promtool test rules deploy/monitoring/edge-alerts.test.yml`.

# API backlog alerts

Set `API_METRICS_ADDR` (for example `:2113`) and scrape that listener's `/metrics`
with the rules in `api-alerts.yml`. It has no authentication, so keep it on an
internal network. Each scrape queries the database, and a failed read fails the
scrape. The 7-day thresholds live in the rule file.

Validate with `promtool check rules deploy/monitoring/api-alerts.yml` and
`promtool test rules deploy/monitoring/api-alerts.test.yml`.
