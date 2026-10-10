# Monitoring

Every bnbloc service exposes Prometheus text metrics at `/metrics`, a pure
liveness probe at `/healthz` and a readiness probe at `/readyz`
(`internal/obs`, no client library). This page lists the metrics, where to
scrape them, and the shipped alert rules.

Shipped files:

- [`deploy/monitoring/prometheus.yml`](../deploy/monitoring/prometheus.yml) —
  scrape configuration for the compose deployment (one job per service);
- [`deploy/monitoring/alerts.yml`](../deploy/monitoring/alerts.yml) —
  alerting rules (loaded by `rule_files` in the scrape config).

Validate after editing: `promtool check config deploy/monitoring/prometheus.yml`
(it also checks the rule file).

## Where to scrape

| Service | Target (compose) | Listener |
| --- | --- | --- |
| gateway | `gateway:9090` | internal metrics listener only (`BNBLOC_METRICS_ADDR`, default `:9090`); the public port answers `/metrics` with 404 |
| room | `room:8080` | main listener (private network) |
| activity | `activity:8081` | main listener |
| realtime | `realtime:8082` | main listener |
| shareagent | `shareagent:8083` | main listener |
| relay | `relay:8085` | main listener |

Setting `BNBLOC_METRICS_ADDR` on an internal service moves its `/metrics` to
that address too. Never route any metrics port from the public edge. In
Kubernetes scrape the chart's Services (the gateway's `metrics` port,
`gateway.metricsPort`). The all-in-one image runs every service in one
container: scrape the gateway's `:9090` and the others on their loopback
listeners from a sidecar, or run the split deployment.

Every series carries `service="bnbloc-<name>"`.

## Metrics

### HTTP (every service)

| Metric | Type | Labels |
| --- | --- | --- |
| `bnbloc_http_requests_total` | counter | `method`, `route`, `status` |
| `bnbloc_http_request_duration_ms` | histogram | `method`, `route`, `status`, `le` |

`route` is a bounded label: path segments outside the route vocabulary
(`routeWords` in `internal/obs/metrics.go`) and every segment after an id
collection collapse to `:id`, so ids and secrets never become labels. A new
route word missing from the vocabulary only reads as `:id`.

### Realtime

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bnbloc_realtime_sse_connections` | gauge | `kind` = `member` \| `bridge` | Open room event streams. `member`: a presence-counting client (web, desktop). `bridge`: a `?presence=0` observer (a member's bridge, or `bnbloc smoke`). |
| `bnbloc_realtime_events_published_total` | counter | `kind` | Events published to room streams, by event kind; kinds outside the known set count as `other`. |
| `bnbloc_notification_deliveries_total` | counter | `outcome` = `delivered` \| `retry` \| `failed` | Notification delivery attempts by this replica's outbox worker (`failed` = given up). |
| `bnbloc_notification_outbox_items` | gauge | `state` = `queued` \| `retrying` \| `delivered` \| `failed` | Outbox items, every tenant: pending never attempted, pending after a failed attempt, and finished items still retained (pruned after 7 days). Refreshed every 30 s. |
| `bnbloc_notification_outbox_oldest_pending_age_seconds` | gauge | — | Age of the oldest pending outbox item (0 when none). Refreshed every 30 s. |

### Activity

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bnbloc_bridge_heartbeats_total` | counter | — | Bridge heartbeats accepted by this replica (`POST /v1/bridge/heartbeat`). |
| `bnbloc_bridges_online` | gauge | — | Bridges of every tenant whose last heartbeat is within the offline window (30 s) and whose status is online. Refreshed every 15 s. |

### Shareagent

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bnbloc_borrow_decisions_total` | counter | `decision` = `allow` \| `deny`; `reason` = `none` or a deny reason (`owner_offline`, `quota_exhausted`, `cost_exhausted`, `concurrency_full`, `model_out_of_range`, `capability_denied`, `grant_invalid`, `binding_revoked`, `org_quota_exhausted`, `other`) | Every audited borrow decision: the lease path and the bridge's accountability record of a borrowed turn. |

### Aggregation across replicas

Gauges read from the shared database (`bnbloc_bridges_online`,
`bnbloc_notification_outbox_*`) report the same value on every replica of a
service — aggregate with `max()`. Counters and the SSE gauge are per replica —
aggregate with `sum()`. realtime is a singleton (in-process hub), so its
per-process series are already deployment-wide.

Every series that an alert reads is registered at zero at startup, so a
`== 0` or `rate()` rule sees a sample before the first event.

## Alert rules

| Alert | Fires when | Severity |
| --- | --- | --- |
| `BnblocServiceDown` | a scrape target is down for 2 min | critical |
| `BnblocHigh5xxRate` | a service answers more than 5 % 5xx for 10 min (with at least 0.1 req/s) | warning |
| `BnblocNoOnlineBridges` | no bridge online for 15 min | warning |
| `BnblocNotificationFailures` | more than 5 notifications given up within an hour | warning |
| `BnblocNotificationOutboxStuck` | the oldest pending notification is older than 15 min | warning |
| `BnblocNoMemberStreams` (optional) | no member stream open for 30 min during business hours (UTC 08–18, Mon–Fri; adjust or delete) | info |

Thresholds are starting points. Route the alerts with your Alertmanager. For
the end-to-end message path, the nightly smoke (scheduled inside the
deployment — [production-deployment.md](./production-deployment.md#nightly-smoke)
— or by the [CI workflow](./ci.md#nightly-smoke)) complements these rules: it fails (and alerts the platform admins through
the installed notifiers) when chat, history or an agent reply break even
though every service is up.
