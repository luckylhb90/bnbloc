# Live monitor: a global real-time board on Redis (2026-10-24)

Status: designed, not built. Owner: this repository (the obs snapshot, the
publisher, the monitor service, the snapshot contract). The sample board lives in
its own repository; the QA Redis and its ACL users live in
the private components repository. Amends the "no Redis or NATS" note of
`docs/prd/2026-10-10-enterprise-scale.md` §2 for the monitoring path only (see §3).

## 1. Why

Operators and the demo need to watch the whole platform move at once: request
rate, errors, live connections, borrows, notifications, per service and across
every replica, updating every few seconds.

What exists today, checked in the code:

- Each service serves Prometheus text at `/metrics` (`internal/obs`), and the
  shipped scrape interval is 30 s (`deploy/monitoring/prometheus.yml`). Prometheus
  is pull-based and has no push path to a browser.
- Per-feature dashboards (the team agent dashboard in `services/activity`, the
  platform usage view in `services/shareagent`) each read their own store. None
  is a cross-service live view.
- Redis is not in `go.mod`, the compose files or the k8s chart. Nothing in the
  product depends on it.

## 2. What it is (end state)

### 2.1 One owner for numbers

The `obs` registry (`internal/obs/metrics.go`, `series.go`) stays the only owner
of every number. It gains one snapshot method that returns the same data
`Handler` renders, so `/metrics` and the live path read one structure. Nothing
increments a second counter. Request paths never call Redis.

A series declares how instances combine, once, where it is registered: `sum` for
counters and per-replica gauges, `max` for gauges read from a shared database
(the rule `docs/monitoring.md` already states for scraping).

### 2.2 Redis layout (prefix `mon:`, ACL-scoped)

| Key | Type | Written by | Meaning |
|---|---|---|---|
| `mon:inst:<service>:<instance>` | HASH, `EXPIRE 5` | each service, every 1 s, one `MULTI` | Series name with labels → value (cumulative for counters, current for gauges); `_meta` fields: build version, process start, schema `v`. |
| `mon:events` | STREAM, `MAXLEN ~ 500` | any service's emitter, the monitor | Feed events: a kind from a fixed enum plus numbers. |

Liveness is the key's existence. Redis expires the key, so no replica's clock is
compared with another's. A new process gets a random instance id, so a restart
never overwrites the old instance's hash.

### 2.3 The publisher (`internal/live`, new)

- A goroutine per enabled service ticks every second and writes its snapshot in
  one `MULTI` (`HSET` + `EXPIRE`). Redis write load is instances × 1 pipeline per
  second, whatever the traffic.
- `Emit(kind, attrs)` is non-blocking: it writes into a bounded in-memory ring
  and returns. A full ring drops the oldest event and counts it.
- Enabled only when `BNBLOC_LIVE_REDIS_URL` is set. Off by default.
- The only importer of the Redis client. `internal/obs` stays dependency-free.
- Errors: counted, logged at most once per minute, the latest snapshot kept and
  nothing queued. Retry backoff is capped at 5 s.

### 2.4 The monitor service (`services/monitor`, new)

- One goroutine reads Redis every second: `SCAN MATCH mon:inst:*` for the
  instance hashes, `XREAD` for new events. Every browser shares this one read.
- Rates come from per-instance counter deltas. A value that decreased (a restart)
  counts as a reset: the new value is the delta. An instance that appeared or
  disappeared between two samples contributes nothing to that tick's delta. No
  rate is ever negative.
- Latency p95 per service from the histogram bucket deltas (approximate, labelled
  as such).
- Output: a snapshot per tick (`bnbloc.live.snapshot/v1`, §2.6), pre-aggregated per
  service, served over SSE at `GET /internal/v1/live`. Clients keep a bounded
  buffer; a client that falls a buffer behind is closed, as the realtime streams
  already do.
- The monitor derives `instance.started` and `instance.stopped` itself from the
  set of live instance keys.

### 2.5 Gateway route

`GET /v1/platform/live` is gated by `rbac.PermManagePlatform` at the gateway (the
pattern of the existing super-admin routes, `services/gateway/routes.go`) and
proxied to the monitor with `FlushInterval = -1`, as room event streams already
are. Per super-admin and total connection caps. The monitor trusts only the
gateway's signed internal identity (`internal/svcauth`).

### 2.6 Snapshot contract (`bnbloc.live.snapshot/v1`)

```json
{
  "v": 1,
  "at": "<monitor clock, RFC 3339>",
  "services": [
    {
      "name": "gateway",
      "instances": 2,
      "reporting": 2,
      "series": { "requests_per_second": 42.0, "errors_5xx_ratio": 0.001, "p95_ms": 118 }
    }
  ],
  "events": [
    { "id": "<stream id>", "kind": "instance.stopped", "service": "realtime", "at": "..." }
  ]
}
```

Documented in `docs/architecture/live-monitor.md` (when built) with a conformance
vector. Unknown fields are ignored; a missing series is absent, never `0`.

### 2.7 Feed event kinds (v1, code-owned enum)

`instance.started`, `instance.stopped` (monitor-derived), `bridge.online`,
`bridge.offline` (activity, on a status change, deduplicated per 10 s), `borrow.denied`
(reason only), `notification.given_up`, `retention.pass`, `search.index_dropped`,
`shard.peers_changed`, `platform.deployed`. Attributes are numbers and enum values:
no ids, no names, no text. An unknown kind is dropped and counted.

### 2.8 The board (sample, own repository)

Built on the public contract only. Tiles: requests/s, 5xx ratio, p95, SSE
connections, bridges online, borrow allow/deny, outbox queued. A per-service table
with `reporting / instances` and the data age of each instance. A live feed. It
shows **"no data"** or **"disconnected"** when the stream is down, never zeros.
It ships no fake data: a demo runs `cmd/bnbloc-loadtest` against a real deployment
so the numbers move for real.

## 3. Not in this step

- Tenant, room or member breakdowns. v1 has no labels carrying ids or tenants.
  Drill-down needs its own RBAC and audit design, not a label added for the demo.
- History and alerting. They stay in Prometheus and Alertmanager; this path reads
  no Prometheus data.
- Sub-second latency. The budget is about 1 s publish + 1 s monitor tick + network:
  2–3 s p95 is the target, and the board says so.
- Desktop bridges as instances. A bridge is a member's machine, not a service;
  its online count arrives as the activity gauge.
- Redis as a source of truth for anything, or a product code path that reads
  `mon:*`.

**Amendment.** `docs/prd/2026-10-10-enterprise-scale.md` §2 keeps room ownership,
sequencing, presence and routing in-process "no Redis or NATS". This design does
not touch any of them. Redis here carries only the monitoring snapshot and the
feed, and nothing in room or message handling depends on it.

## 4. Placement table

| Part | Content / component / platform | Why (platform: the named reason) | What an organization would swap |
|---|---|---|---|
| Snapshot of the obs registry, per-series aggregation | Platform (`internal/obs`) | Events only the platform sees: its own counters, in-process. One owner of numbers. | Nothing; it is the source |
| Publisher, `Emit`, the Redis driver (`internal/live`) | Platform, behind a `Sink` seam | Events only the platform sees. The transport is a seam: Redis is one `Sink`. | Another bus (NATS, Kafka) or none |
| Monitor service: rates, aggregation, SSE fan-out | Platform (`services/monitor`) | Events only the platform sees (cross-replica state); isolation: the Redis read credential stays out of the gateway | Not needed by an org that runs Grafana on the same metrics |
| Super-admin gate on `/v1/platform/live` | Platform | Identity: `PermManagePlatform` already exists; the gateway is the attested caller | Nothing |
| Snapshot contract and its conformance vector | Platform contract (`docs/architecture`, tests) | Contracts with a conformance suite: any board must read the same thing | Any board |
| Board UI (tiles, table, feed, assist) | Sample, own repository, on the public contract | A presentation: no platform code names a tile, a colour or a layout | The org's own board or Grafana |
| Redis deployment, ACL users, secrets, TLS | Deploy: generic sample in public `deploy/`, QA values in the private components repository | Internal hosts and credentials never enter the public tree | The org's Redis |
| Demo traffic | Existing sample tool: `cmd/bnbloc-loadtest` | Already drives realistic room traffic | Any load generator |

Name test: grep the platform for "dashboard", "tile" and "board": no hits outside
the sample. Fork test: an org with Grafana changes nothing in the platform. Replace
test: a second `Sink` installs beside Redis.

## 5. Design review

| Lens | Answer |
|---|---|
| Actors | Readers: super admins only (`PermManagePlatform` at the gateway); tenant members and admins are refused like every platform route. Writers: each service's publisher under its own Redis ACL user (write `mon:inst:*`, `XADD mon:events`, nothing else); the monitor under a read-only user. Bridges and desktops are not instances and never write. A shared demo screen is a super admin's session. |
| Time and lifecycle | Liveness is the key TTL (5 s), so no cross-machine clock is compared. A restart gets a new instance id; its counters restart at zero and the reset rule covers it. An instance that vanishes leaves the sums in that tick and never produces a negative rate. The monitor is stateless: a restart re-reads Redis, and the first tick shows rates as "warming up", not `0`. A Redis restart shows a gap of about one tick, then resumes. A client that connects during an incident reads the last events from the stream (within `MAXLEN`). |
| Scale and cost | Redis writes: instances × 1 pipeline per second, independent of traffic (estimate: a few dozen service instances, a few hundred fields each). Browsers: one Redis read per tick shared by every client; the snapshot is pre-aggregated per service, so per-client bytes stay small. Caps: per super admin and total SSE connections. Accumulation: the stream is capped, the hashes expire, the in-memory ring is bounded. Nothing grows over weeks. |
| Failure and recovery | Redis unreachable: publisher counts the error, keeps only the latest snapshot, backs off to 5 s; no request waits on it (test: Redis blackholed, request p99 unchanged). Monitor down: the gateway answers 503 and the board says "disconnected". Partial visibility: a `MULTI` per instance means a reader may see one tick of skew across instances; documented, not hidden. Silent breakage (an ACL rotated, an env var missing): the instance drops off the board, the board shows "N of M reporting", and `instance.stopped` appears in the feed. Retries are safe: a publish overwrites, and feed duplicates are removed by stream id. |
| Boundaries and privacy | The snapshot carries registered series (bounded labels) and feed kinds from the enum. No ids, names, IPs, message text, tenants or rooms in v1. Redis is on the private network only, with TLS and per-role ACLs; the password comes from env or the secret store and is never logged or put in a URL. Not reachable from the public edge (the same rule as `/metrics`). Counts only, so deleting a tenant or a room needs no change here. |
| Abuse and untrusted input | Read-only endpoint; no client writes to Redis or the monitor; no request body. Connection caps and idle heartbeats; a slow client is closed. Feed attributes are numbers and enum values, so no free text reaches the board. Unknown kinds are dropped and counted. The board escapes everything it renders (its own repository, its own test). |
| One owner | Numbers: the obs registry only, aggregation declared where registered. Liveness: the Redis TTL only. Event kinds: one enum in `internal/live`; emitters use its constants. Architecture test: only `internal/live` and `services/monitor` import the Redis client, and no product package reads `mon:*`. The monitor keeps no list of series of its own. |
| Defaults and fallbacks | `BNBLOC_LIVE_REDIS_URL` unset: no publisher, the route answers "not configured", the board says so. No demo dataset is ever shown. A series an older build does not publish is "not reported", never `0`. A first rate is "—". |
| External contracts | Redis 7 features used: `MULTI`, `SCAN`, streams, ACL. Tested against a real Redis for the ACL denials (a publisher cannot read another key, the monitor cannot write) and against an in-memory fake for the logic. The snapshot is versioned (`v`); the board pins a version. Prometheus scraping is unchanged. The Redis client is the one new dependency (§7). |
| Load on users and agents | No model in the path, no agent prompt, tool or rule. Nothing added to any room. |
| Release and compatibility | Additive and off by default. Old services without the publisher show as "not reporting", which is true. A new publisher against an old monitor is fine (unknown fields ignored). No database migration, no in-use state touched. Rollout: QA with Redis first, then the env var per service. Rollback: unset the variable (or stop Redis). Services that do not enable `internal/live` do not link the Redis client. |
| Observability | Monitor metrics: `bnbloc_live_publish_total{result}`, `bnbloc_live_publish_duration_ms`, `bnbloc_live_feed_dropped_total`, `bnbloc_live_clients` (gauge), `bnbloc_live_instances_reporting{service}` (gauge, alertable). Logs: `event=bnbloc.live.publish_failed`, at most once per minute. The board shows data age per instance. Breaking it on purpose (stop Redis, kill an instance) must be diagnosable from the board and the metrics alone. |
| AI capability | Entry point: an assist on the board ("what changed in the last five minutes?") that reads the snapshot and the feed, which are counts and kinds only. Read-only: it proposes, it never acts. Uses the super admin's own budget. With the assist model off, the board works and the assist says why it is off. Not used for the tiles: a number has no decision in it. |

**Pre-mortem.** It shipped and a month later it failed badly. The three most likely
reasons, and what this design does about each:

1. **The board stays green while publishing is broken** (a rotated ACL, a missing
   env var). Numbers look fine because they are old. Answer: the board shows
   "N of M reporting" and data age; the instance-count gauge is alertable; the
   publish error counter is on the board; the acceptance test breaks a publisher's
   credential on purpose and checks the board says so.
2. **Redis becomes a hard dependency.** A product path starts reading `mon:*` or
   waits on the publisher, and a Redis blip becomes a product outage. Answer: the
   architecture test (only two packages import the client; no product reader), and
   the blackholed-Redis test measuring request latency.
3. **The board grows into a second metrics store.** Tenant or room labels get added
   "for the demo", or history is kept here. That is a privacy leak and two sources
   that disagree. Answer: §3 non-goals, the obs rule that ids never become labels,
   and history staying in Prometheus.

## 6. Acceptance

- Redis unset: no behaviour change; no Redis import reachable from `internal/obs`.
- Redis set, two gateway replicas and two realtime replicas: all four report; each
  board row shows `reporting / instances`.
- `kill -9` one realtime replica: it leaves the board within 6 s and
  `instance.stopped` appears in the feed.
- A counter reset (restart) produces no negative rate, in a test with a
  decreasing series.
- Redis stopped for 60 s: request p99 within noise of the baseline; the board shows
  "disconnected"; on recovery it resumes within 3 s.
- 100 SSE clients: one Redis read per tick, counted by a metric.
- A non-super-admin gets the same refusal as any platform route. The snapshot
  contains no ids (a test greps every label and feed attribute).
- The demo: `cmd/bnbloc-loadtest` running; requests/s and room messages visibly move
  within 3 s.

## 7. Landing (slices, each reviewable alone)

1. `obs` snapshot and the aggregation declaration, with tests. No Redis.
2. `internal/live`: publisher, `Emit`, the `Sink` seam, the Redis `Sink`, fake-sink
   tests.
3. `services/monitor`: aggregation, SSE, the gateway route and its super-admin gate.
4. The contract doc and conformance vector in `docs/architecture`.
5. The sample board, in its own repository.
6. QA Redis and its ACL users in the private components repository; enable on QA with
   `optin`-style rollout per service first.

New dependency: `github.com/redis/go-redis/v9`, confined to `internal/live`. The
alternative is a hand-written RESP client for the six commands used, in keeping
with the dependency-free metrics; the trade-off is TLS, pipelining and reconnect
behaviour, which the client library already handles.

## 8. Open decisions

| # | Decision | Recommendation |
|---|---|---|
| 1 | Is Redis a product dependency? | No. Optional and off by default. |
| 2 | Where does the board live? | Sample in its own repository (the rule for use cases). A page in the admin console would be a platform surface and needs its own named reason. |
| 3 | Is "global" cross-tenant totals only? | Yes for v1. Per-tenant drill-down later, with RBAC and audit. |
| 4 | Redis client: go-redis v9 or hand-written RESP? | go-redis v9, confined to one package. |
