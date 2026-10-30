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

**Profiling.** `BNBLOC_PPROF=1` serves Go's profiler (`/debug/pprof/`)
where `/metrics` is served: the internal metrics listener, or an internal
service's main listener; never a public one. For a CPU profile of realtime
under load: `curl -o cpu.pprof http://realtime:8082/debug/pprof/profile?seconds=20`,
then `go tool pprof -top cpu.pprof`. Leave it off in production unless you
are looking at something.

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
| `bnbloc_realtime_sse_connections` | gauge | `kind` = `member` \| `bridge` \| `app` | Open room event streams. `member`: a presence-counting client (web, desktop). `bridge`: a `?presence=0` observer (a member's bridge, or `bnbloc smoke`). `app`: an interactive app's change feed (the gateway's `GET ~app/events`). |
| `bnbloc_realtime_events_published_total` | counter | `kind` | Events published to room streams, by event kind; kinds outside the known set count as `other`. |
| `bnbloc_realtime_feed_commits_total` | counter | `result` = `ok` \| `error` | Room feed group commits: each stores a batch of one room's durable events (chat, agent replies, imports) in one multi-row insert. `error` fails every member of the batch (store outage, or the scale-out fence); clients retry idempotently. |
| `bnbloc_realtime_sse_overflows_total` | counter | — | Room streams closed because the client fell a whole buffer (64 events) behind. The client reconnects and catches up from history, so nothing is lost, but a steady rate means streams cannot keep up (slow clients or network, or an overloaded realtime). |
| `bnbloc_realtime_stream_lane_events_total` | counter | `lane` | Room events on people's lanes streams (channels PRD §2.4) by lane: `full` delivered, `activity` folded into a `channel_activity`, `activity_sent` the `channel_activity` events written, `none` dropped (a closed channel's typing or job progress). `activity` against `full` is what the lanes save. |
| `bnbloc_realtime_sse_gaps_total` | counter | — | Room streams that fell a whole buffer behind once and were told to fetch the difference (`stream_gap`) instead of being closed. A rate here with few overflows is streams riding out bursts; both rising is clients that cannot keep up. |
| `bnbloc_realtime_chat_step_seconds` | histogram | `step` | A chat post's time by step: `access` (membership, channel, read access), `admit` (rules, who posts, slow mode), `commit` (waiting for the room's batch, its commit and delivery), `after` (mentions, thread counts, the agent job). `commit` is the database; any other step above a millisecond is a regression of the room state at the owner. |
| `bnbloc_realtime_feed_commit_seconds` | histogram | — | One room batch's database commit. |
| `bnbloc_realtime_feed_deliver_seconds` | histogram | — | One room batch's delivery to the room's streams (the hub's lock is held that long). |
| `bnbloc_realtime_feed_commit_events_total` | counter | — | Durable events stored by `ok` commits. Divided by `feed_commits_total{result="ok"}` it is the mean batch size: near 1 when rooms are quiet, higher when one room's messages arrive faster than a commit takes. |
| `bnbloc_notification_deliveries_total` | counter | `outcome` = `delivered` \| `retry` \| `failed` | Notification delivery attempts by this replica's outbox worker (`failed` = given up). |
| `bnbloc_notification_outbox_items` | gauge | `state` = `queued` \| `retrying` \| `delivered` \| `failed` | Outbox items, every tenant: pending never attempted, pending after a failed attempt, and finished items still retained (pruned after 7 days). Refreshed every 30 s. |
| `bnbloc_notification_outbox_oldest_pending_age_seconds` | gauge | — | Age of the oldest pending outbox item (0 when none). Refreshed every 30 s. |

### Retention

Data retention runs on realtime ([production-deployment.md](./production-deployment.md#data-retention));
one replica at a time does a pass, so aggregate the counters with `sum()`.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bnbloc_retention_deleted_total` | counter | `item` = `room_messages` \| `room_messages_redacted` \| `message_tasks` \| `agent_messages` \| `attachments` \| `artifact_versions` \| `artifact_shares` \| `shared_agent_audit` \| `extension_audit` \| `agent_jobs` \| `blobs` \| `search_documents` | Rows (and blobs) deleted by retention. `room_messages_redacted` counts a room's newest message emptied instead of deleted (it holds the room's sequence). `search_documents` counts documents removed from the OpenSearch index with the messages. |
| `bnbloc_retention_blob_bytes_deleted_total` | counter | — | Bytes of unreferenced blobs the collection removed. |
| `bnbloc_retention_runs_total` | counter | `outcome` = `ok` \| `error` \| `skipped` | Retention passes on this replica; `skipped` = another replica held the lock. An `error` pass logs `event=bnbloc.retention.error` with what failed and goes on with the other tenants. |
| `bnbloc_retention_last_run_timestamp_seconds` | gauge | — | When this replica last finished a pass. Across replicas, `max()` older than a few hours means no replica is cleaning up. |

### Search (realtime)

Registered on every realtime replica; non-zero only with the OpenSearch
backend ([production-deployment.md](./production-deployment.md#search-backend)).

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bnbloc_search_index_updates_total` | counter | `result` = `ok` \| `error` \| `dropped` | Messages handed to the index updater (new rows of the rooms this replica owns, edits, deletes). `error`: failed after three attempts (logged as `event=bnbloc.search.update_failed`); `dropped`: the 10,000-message queue was full (`event=bnbloc.search.update_dropped`). Either leaves the index behind until `bnbloc search reindex`; results stay correct (every candidate is re-checked in MySQL) but may miss new or edited messages. Writes never wait for or fail on the index. |
| `bnbloc_search_index_queue_length` | gauge | — | Messages waiting for the index on this replica. Steadily near 10,000 means the cluster cannot keep up. |
| `bnbloc_search_stale_candidates_total` | counter | — | Search candidates the store rejected: deleted, edited away or removed before the index caught up. A steady rate without matching update errors points at a lagging cluster. |

### Scale-out (realtime, relay)

Registered when the service runs with peers (`BNBLOC_<SVC>_PEERS`).

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bnbloc_shard_peers` | gauge | — | Live replicas in this replica's ownership view. Every replica should report the same value; a disagreement that persists beyond a few refreshes (5 s each) means replicas cannot probe each other. |
| `bnbloc_shard_forwarded_total` | counter | — | Requests this replica proxied to the replica that owns their key (room, member, relay session). |
| `bnbloc_shard_served_unowned_total` | counter | — | Forwarded requests served here although this replica does not own the key (two views briefly disagreed). Occasional increments during scaling are expected; a steady rate means the views do not converge. |

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
aggregate with `sum()`; that includes realtime when it runs several replicas
(each room's streams and publishes are counted only on its owner, so the sum
counts every event once; forwarded requests also appear in the receiving
replica's `bnbloc_http_requests_total`).

Every series that an alert reads is registered at zero at startup, so a
`== 0` or `rate()` rule sees a sample before the first event.

### Store (read replica routing)

Registered by every service that starts `store.RunReplicaMetrics` with a configured
replica (activity and shareagent today). Per process; see
[Read replica](./production-deployment.md#read-replica).

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bnbloc_store_replica_usable` | gauge | — | `1` when opted-in reads may use the replica (threads running, lag within the bound, check fresh), `0` when the primary serves them. |
| `bnbloc_store_replica_lag_seconds` | gauge | — | Apply lag as the last check read it. |
| `bnbloc_store_reads_total` | counter | `target` = `replica` \| `fallback` | Opted-in read-only queries by where they were served. `fallback` counts reads that wanted the replica while it was configured but not usable. |

Suggested alert: `max(bnbloc_store_replica_usable) == 0` for 10 minutes while
`bnbloc_store_reads_total{target="fallback"}` is rising.

### Organization board (bnbloc-pulse)

Registered by the Pulse consumer (`services/pulse`). One consumer per deployment; see
[Environment variables](./environment-variables.md) for its settings.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bnbloc_pulse_events_total` | counter | `result` = `applied` \| `duplicate` \| `invalid` | Stream entries read: counted, skipped as a redelivery, or refused by the schema and dropped. |
| `bnbloc_pulse_flush_total` | counter | `result` = `ok` \| `error` | Rollup flushes: committed and acked, or kept for retry. A steady `error` rate means the database is not taking the rollup. |
| `bnbloc_pulse_pending` | gauge | — | Stream messages read but not yet acked. It grows while flushes fail, and reading pauses at the consumer's bound. |
| `bnbloc_pulse_live_errors_total` | counter | — | Live-state writes that failed (presence and job state; self-healing). |

Suggested alert: `rate(bnbloc_pulse_flush_total{result="error"}[10m]) > 0` for 10 minutes.

### Organization board emitter (product services)

Registered by every service that sends Pulse events (activity, realtime, room) when
`BNBLOC_PULSE_REDIS_URL` is set. Per process.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `bnbloc_pulse_emit_total` | counter | `result` = `accepted` \| `flushed` \| `invalid` \| `attr_dropped` \| `dropped_queue` \| `dropped_sink` \| `dropped_breaker` \| `sink_errors` | Events by outcome. Accounting holds: `accepted = flushed + dropped_sink + dropped_breaker` once the queue is empty. A Redis outage shows as `dropped_breaker`. |
| `bnbloc_pulse_emit_queued` | gauge | — | Events waiting for the batcher. |

Suggested alert: `rate(bnbloc_pulse_emit_total{result="dropped_breaker"}[5m]) > 0` for 5 minutes.

## Alert rules

| Alert | Fires when | Severity |
| --- | --- | --- |
| `BnblocServiceDown` | a scrape target is down for 2 min | critical |
| `BnblocHigh5xxRate` | a service answers more than 5 % 5xx for 10 min (with at least 0.1 req/s) | warning |
| `BnblocNoOnlineBridges` | no bridge online for 15 min | warning |
| `BnblocNotificationFailures` | more than 5 notifications given up within an hour | warning |
| `BnblocNotificationOutboxStuck` | the oldest pending notification is older than 15 min | warning |
| `BnblocSearchIndexBehind` | OpenSearch index updates failed or were dropped within 15 min (run `bnbloc search reindex` once the cluster is healthy) | warning |
| `BnblocNoMemberStreams` (optional) | no member stream open for 30 min during business hours (UTC 08–18, Mon–Fri; adjust or delete) | info |

Thresholds are starting points. Route the alerts with your Alertmanager. For
the end-to-end message path, the nightly smoke (scheduled inside the
deployment — [production-deployment.md](./production-deployment.md#nightly-smoke)
— or by the [CI workflow](./ci.md#nightly-smoke)) complements these rules: it fails (and alerts the platform admins through
the installed notifiers) when chat, history or an agent reply break even
though every service is up.
