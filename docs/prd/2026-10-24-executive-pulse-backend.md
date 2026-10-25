# Pulse backend: how it is built without touching the product (2026-10-24)

Status: plan, not built. Companion to `docs/prd/2026-10-24-executive-pulse.md`
(what the board shows). This document is how the backend gets its data, and
why the product cannot be slowed or broken by it. Design lenses are answered
in the PRD; this file covers the implementation and its gates.

## 1. Principles (non-negotiable; each one has a gate in §7)

- **P1. Writes never wait for Pulse.** After a product write commits, Pulse gets
  one non-blocking enqueue into a bounded in-memory queue. No database work, no
  network call, no lock on the request path. A full queue drops the event and
  counts the drop.
- **P2. Live data is lossy; the numbers are corrected.** Live effects (the
  swarm, the field, the pump) come from the event stream and may miss events
  under pressure. Rollups are corrected by a reconciler that recounts from the
  authoritative tables. The board's totals therefore converge even when the
  stream drops.
- **P3. Pulse is removable.** Unset `BNBLOC_PULSE_REDIS_URL`: no goroutine, no
  connection, no allocation beyond a nil check. Pulse down: the board goes
  stale and says so; the product is unaffected.
- **P4. Everything is bounded.** Queue length, batch size, Redis memory (stream
  `MAXLEN`), rollup rows per flush, database statements per minute, reconciler
  rows per tick, CPU per event. A figure with no bound is not allowed in review.
- **P5. No triggers, no synchronous outbox in business transactions.** A database
  trigger or an outbox row written inside the product transaction adds write
  amplification and lock time to every message and job. Rejected.
- **P6. One-way dependency.** The product emits; Pulse reads. The product never
  reads Pulse, and no product code path imports the Pulse client. Architecture
  test enforces it (§7, G7).

## 2. Architecture

```
product replicas (realtime, room, activity, bridge-facing services)
   │  after commit:  pulse.Sink.Observe(event)    ── O(1), non-blocking
   ▼
internal/pulse Emitter  (per replica)
   bounded channel (8192) ─► batcher goroutine ─► XADD pulse:events (pipelined,
   drop + counter when full / Redis down          every 100 ms or 256 events)
                                                  MAXLEN ~ 200 000
                                                       │
                                                       ▼
services/pulse (own deployment, consumer group)
   ├─ live state (Redis, keyspace pulse:*):
   │    presence ZSET (member HMAC → last seen), live job set, counters, HLL zones
   ├─ rollups: pulse_minutes (MySQL), batched upsert every 15 s
   ├─ reconciler: every 5 min, per tenant, recounts the settling minutes from
   │    agent_jobs / artifacts / room_messages (indexed, chunked, budgeted) and
   │    overwrites those rows
   └─ snapshot + SSE ─► gateway (`read_org_pulse` gate) ─► the board
```

Why these choices:

- **Redis Streams, not a new broker.** The engineering monitor PRD already
  brings Redis in (`docs/prd/2026-10-24-live-monitor.md`). Pulse adds a separate
  keyspace and a separate consumer group on the same instance. A stream is
  bounded by `MAXLEN` and survives a consumer restart; the product holds no
  reference to it.
- **A separate `services/pulse` deployment**, not code inside activity or
  realtime. Its CPU, memory and failures are then its own. A slow aggregation
  cannot starve chat delivery, and a Pulse bug cannot take a product replica
  down.
- **Reconciliation, because the stream is lossy by design.** Without it, a
  dropped event is a wrong number forever. With it, the board is wrong for at
  most one reconcile interval (5 min), and only on the settling minutes.

## 3. Event contract (v1)

One envelope, built only from enums, numbers and opaque ids:

```json
{ "v": 1, "id": "<job_id>:<status>" | "<event uuid>", "tenant": "<tenant_id>",
  "kind": "job.claimed", "at": 1761300000000, "attrs": { ... } }
```

| Kind | Emitted when | Attributes (no names, no text) |
|---|---|---|
| `job.claimed` | a bridge claims a job (lease set) | `core`, `phase` (claimed) |
| `job.finished` | a job reaches `replied`, `failed`, `timed_out` or `cancelled` | `core`, `status`, `failureReason` (enum), `durationMs`, `reported` (bool), `tokens` (input, output, cache; absent if not reported), `costUsd` (absent if not reported) |
| `job.swept` | the lease sweeper reclaims a lapsed job | `core`, `reason` (enum) |
| `artifact.version` | an artifact version is created | `kind`, `isNew` (bool) |
| `message.sent` | a room message is persisted | `channelType` (enum: room, people, agent) |
| `app.written` | an app document is written | `public` (bool) |
| `presence.seen` | a member's presence is refreshed (join, or periodic) | `member` (HMAC of the user id, keyed by a Pulse secret), `zone` (IANA name, or "other") |
| `presence.left` | a member's last room presence ends (after the grace timer) | `member` (HMAC) |
| `member.joined` | a person becomes a member of an organization | `source` (enum: manual, directory) |

Rules: the attribute set is closed (unknown keys are dropped at the emitter), an
attribute may never carry free text, and `member` is always the keyed hash. The
consumer rejects events for a tenant it does not know rather than creating one.

Idempotency. Job events are monotonic per job (the store's state machine), so
the consumer keeps the last status per job and ignores repeats. Other kinds carry
an event id and are de-duplicated with `SET NX pulse:seen:<id> EX 3600`. A
dropped or duplicated event is repaired by the reconciler either way.

## 4. Hook points (verified in the code)

| Signal | Where it hooks | Why it is safe |
|---|---|---|
| Room messages, artifact and presence events | `services/realtime/service.go` `publish()`, after `Hub.PublishCommit` (the durable group commit) and inside `afterPublish`, which is already documented as best-effort and non-blocking | Runs after the commit; the observer is the same pattern as `s.Notifications.Observe` |
| Artifact version created | `services/room/artifacts.go` `publishArtifactEventReason` | Already the emit point for artifact events |
| Presence join and leave | `internal/realtime/presence.go` (`Join`, `Leave`, `finalizeLeave`), which already holds a grace timer | The leave is delayed by the grace timer, so flicker is absorbed before Pulse sees it |
| Job claim, finish, sweep | `internal/store/mysql_bridge_jobs.go` writes (claim near line 147, finish near 271, sweep near 305 to 324); the call sites are in `services/activity` | Hook after the store call returns success, never inside its transaction |
| Reply usage | `internal/store/team_agents.go` `ReplyUsageOf`, at reply ingest | After the reply is stored |
| App document writes | `services/room/appdata.go` | After commit (to confirm in slice 2) |

Not verified yet, and to check in slice 2: the exact service-layer call sites
for job transitions, and whether `appdata.go` has a single write point.

## 5. Failure and impact analysis

| Failure | What the product does | What Pulse does | Recovery |
|---|---|---|---|
| Emitter queue full | Nothing (enqueue is a non-blocking send that drops) | Counts `pulse_dropped_total{reason="queue"}` | Reconciler corrects the minute |
| Redis unreachable | Nothing | Batcher backs off; after a bounded buffer it drops and opens a circuit breaker for 30 s | Automatic; reconciler corrects |
| Pulse service down | Nothing | Stream grows to `MAXLEN` and is trimmed; no product effect | Restart; consumer group catches up; reconciler corrects settling minutes |
| Pulse DB writes slow | Nothing | Upserts batch; a slow flush delays rollups, the live state continues | Backpressure inside Pulse only |
| Reconciler heavy on the database | Could add load to the primary | Budget per tick: a row limit, a time limit, a pause between chunks, and a skip if the database reports high latency | Tunable at runtime (§6, decision D2) |
| Schema change | None (additive table) | Creates `pulse_minutes` | Mixed deploy safe: the old code never reads it |
| Event with a bad attribute | Nothing | Dropped at the emitter, counted | Fix the emitter |

Costs, measured in slice 1 (`internal/pulse`, `go test -bench`, 12 cores):

- Enqueue on the product path: about 125 to 130 ns and zero allocations, of which
  validation is about 55 to 65 ns (gate G1: under 200 ns). With a batcher draining
  the same queue on another core it measured 300 to 360 ns: that is channel
  contention with the single consumer, not work the product does.
- Disabled (no Pulse configured): about 12 ns per call and zero allocations.
- Queue memory: about 370 bytes per event, so about 3 MB per replica at the default
  8192 events. The earlier 256-byte estimate was low.
- Redis: one pipeline per flush interval (100 ms) per replica, whatever the traffic.
- Rollup upserts (slice 3): one statement per tenant per 15 s.

## 6. Decisions

Decided 2026-10-24:

- **D1 Redis: resolved, required.** Redis becomes a platform dependency for performance,
  not only for Pulse. The emitter stays non-blocking either way: a Redis outage must not
  block or fail a product request.
- **D2 Read/write split: resolved first.** Pulse waits for the replica routing
  (`docs/prd/2026-10-24-read-replica-routing.md`, built and tested). Pulse's reads are
  opted in to the replica; its writes and the product's stay on the primary.
- **D3 Reconciliation: dropped for now.** Recounting from the authoritative tables costs
  database load the platform cannot yet afford; the stream's loss is accepted. Revisit
  once the replica carries Pulse's reads.
- **D4 Presence: reuse existing machinery.** The grace timer in `internal/realtime/presence.go`
  and a last-seen TTL in Redis, as proposed.

Open (unchanged): the exact call sites for job transitions and app writes (slice 2).

## 6a. Decisions the user owned (history)

- **D1. Redis as a dependency of Pulse only.** Pulse needs Redis; the product
  does not. If Redis is absent, Pulse is off, not degraded. Confirm.
- **D2. Reconciler against the primary or a replica.** The code has no replica
  configuration today (one `BNBLOC_MYSQL_DSN`). Options: (a) run it on the primary
  with the budgets above, (b) add a replica DSN and run it there, (c) reconcile
  from the stream's own counters plus a daily full recount. Recommendation: (a)
  for slices 3 to 4 with conservative budgets, then (b) once a replica exists.
- **D3. Indexes for the reconciler.** Counting artifacts and messages by time
  across a tenant may need an index that does not exist (the existing indexes
  are keyed by room). An index on a large table is a migration with its own
  risk; it needs a maintenance window decision.
- **D4. Presence scope.** "Online" follows the grace timer in
  `internal/realtime/presence.go`. The consumer uses last-seen with a TTL, so a
  missed leave heals itself within the TTL. Confirm the TTL (proposal: 90 s).

## 7. Gates (the proof that the product is not affected)

- **G1 Emitter cost.** `go test -bench` on the emitter: hot-path ns/op and
  allocs/op recorded in the PR. Gate: no allocation on the enqueue path after
  warm-up; enqueue under 200 ns.
- **G2 Load: no change to the product.** Run `cmd/bnbloc-loadtest` against a
  deployment with Pulse off, then on, using the methodology of
  `docs/load-testing.md`: same rooms, same rate, same duration, repeated
  paired runs. Gate: send latency p50, p95 and p99 differences within the run-to-run
  noise of the baseline, and no change in error rate.
- **G3 Chaos: Redis blackholed for 10 min under load.** Gate: product error rate
  and p99 unchanged; `pulse_dropped_total` rises; the board shows "delayed".
- **G4 Chaos: Pulse service stopped for 15 min under load.** Same gate as G3;
  on restart, consumer lag drains and the settling minutes resolve.
- **G5 Correctness.** On a quiet dataset, stream-derived and reconciled counts
  are equal within a stated tolerance. A deliberate drop of 1 % of events is
  repaired by the next reconcile.
- **G6 Kill switch.** With the variable unset, a test asserts no goroutine,
  no Redis connection and no Pulse allocation is created.
- **G7 Architecture.** A test asserts that only `internal/pulse` and
  `services/pulse` import the Pulse Redis client, and that no product package
  reads the `pulse:*` keys.
- **G8 Blocking sink.** A test injects a sink that blocks forever; the product
  request still returns within its normal bound. This is the direct proof of P1.

Rollout follows the release rule: the emitter ships disabled; enabled on one
realtime and one activity replica for 48 hours with G2 repeated on that
deployment; then all replicas; the reconciler last and per tenant.

## 8. Slices (each one reviewable and safe alone)

Status: slice 1 landed (`internal/pulse`, tests G1, G6 and G8 passing; race-clean). Slice 2 landed (hooks, see section 8a). Slice 3 landed (consumer, rollups, snapshot; see section 8b). Load and chaos gates measured in section 8d: G3, G4 and G5 passed; G2 inconclusive on a loaded host.

1. **`internal/pulse`**: envelope, closed attribute set, bounded emitter,
   batcher, metrics, the no-op sink, benchmarks (G1), unit tests (G6, G8). No call
   sites. Zero product effect by construction.
2. **Hook wiring behind the flag**: the hook points in §4, each a single
   `Observe` call after commit. Tests: G8 per hook point; attribute closure.
3. **`services/pulse` consumer**: consumer group, live state, `pulse_minutes`
   (additive migration), batched upserts, snapshot API behind `read_org_pulse`.
   Gates G4 and G5 (stream part).
4. **Reconciler**: settling minutes, per-tenant budgets, overwrite rules. Gate
   G5 in full, and D2/D3 resolved first.
5. **Gateway route and SSE**: the board reads the real API instead of the
   simulation. Gate: the board's numbers match the reconciled rollups.
6. **Load and chaos gates, canary, rollout** (G2, G3, then the rollout above).

## 8a. Slice 2 as built

Hooks are in place behind an optional `*pulse.Emitter` on each service (`Pulse`
field, nil = off). **Nothing sets it yet**, so slice 2 has no effect on running
deployments; slice 3 adds the Redis sink and the configuration that enables it.

Where each event is emitted, and what changed from the table in section 3 and 4:

| Event | Emitted by (after commit) | Notes |
|---|---|---|
| `job.claimed` | activity claim handler, for every job the claim returns | A job the budget refusal then refuses is emitted claimed and then finished (failed). The board sees the same sequence the queue did. |
| `job.finished` | activity status handler; the sweep loop; the budget refusal | Terminal statuses only. Carries status, failure reason (the store's 19 values, or `other`) and duration. **No usage**: usage belongs to the reply. |
| `job.swept` | activity sweep loop | Reason is the sweep's own (`no_bridge_online`, `lease_expired`). |
| `reply.usage` (**new kind**) | activity reply ingest, one per stored agent reply | Replaces the usage attributes that were on `job.finished`. Usage arrives with the reply's own write, not at the job's finish, so it is a separate event. Job notices are skipped. Coverage = replies that report usage. |
| `message.sent` | realtime `PostChat`, after the durable publish | `channelType` is `room` or `agent`. **`people` dropped**: telling a people channel from a room needs a database lookup on the message path, which this design refuses. |
| `presence.seen` / `presence.left` | realtime SSE handler, for human streams only | The first `seen` is synchronous; a refresh every 30 s while open (the consumer's TTL needs it); `left` is emitted after the refresher has exited. `zone` is `other` until clients send a zone (geography is off by default). |
| `artifact.version` | room `publishArtifactEventReason`, for non-thumbnail changes | `isNew` is true for version 1. |
| `app.written` | room create and update, beside `announceAppChange` | The event id carries a digest of the app's collection and document id, never the strings. Deletes are not emitted. |
| `member.joined` | **not wired** | The join path does not carry a source (directory or manual) at a single write point. Deferred until that is settled. |

Tests: each hook is checked against real store and wire values (`services/*/pulse_test.go`,
with `internal/pulse/pulsetest` as the recorder). A drift test checks that every store
failure reason is accepted by the pulse set. The presence test found an ordering bug
(left could land before seen) and it is fixed in the code.

Full repository suite: passes with and without MySQL. One unrelated test
(`TestSearchParityStoreAndOpenSearch`, an index-catch-up deadline) failed once under full
parallel load and passes in isolation and on repeat runs of its package.

## 8b. Slice 3 as built

The consumer is `services/pulse` (`bnbloc-pulse`). The rollup table is `pulse_minutes`
(migration `2026_10_pulse_minutes`, a memory variant, a contract test). The product
services start Pulse with `pulse.FromEnv` (`BNBLOC_PULSE_REDIS_URL`), so nothing emits
until an operator sets it.

What differs from the plan above:

- **Delivery is at-least-once, with a bounded window.** A message is acked only after
  the rollup that counts it has committed. A crash before the commit recovers the
  message (`TestConsumerRecoversUnackedMessagesAfterACrash`). A crash after the commit
  but before the ack can count it twice; the consumer's recent-id window (200 000 ids)
  drops redeliveries within a run, but not across a restart. The board is a dashboard,
  not a bill, so this is accepted.
- **A failed rollup write keeps its deltas and its messages** and the next flush retries
  them once (`TestConsumerRetriesAFailedFlushWithoutDoubleCounting`). A bug the tests
  caught here: recovery re-read the same pending batch forever; it now advances a cursor.
- **Live state is idempotent** (ZADD, ZREM, PFADD), so a redelivered event does no harm.
  Windows bound it: a member is online for 90 s after their last `presence.seen`; a job
  counts as working for 20 min unless its finish arrives; active people is a HyperLogLog
  per UTC day, kept eight days, with no member id stored.
- **Active people is message senders.** `message.sent` carries the sender's member digest.
  Other actions carry no member, so the weekly count is the people who sent a message.
  That is a narrower definition than "meaningful action" in the PRD, and the board should
  say so.
- **Jobs carry a digest of their id** (`job` attribute), so a finish removes the claim it
  belongs to without the id itself. The `waiting` count is not built: no queued event is
  emitted yet.
- **Snapshot** (`GET /v1/tenant/pulse/snapshot`, behind the internal identity): the UTC
  day's totals from the rollup (replica-read), replies by core with coverage (the pump's
  lanes), a data-as-of time, live agents working, people online and the week's active
  people, both withheld below k = 5 and reported as suppressed. The gateway's
  `read_org_pulse` gate is not built yet.
- **Retention:** rollup rows older than `BNBLOC_PULSE_RETENTION` are deleted hourly.

Gates: G7 (only the Pulse packages import the Redis client) is enforced by a test, and
the test was checked against a planted violation. G4 and G5 (stream part) are covered by
the consumer tests. **Not yet run: G2 (load, Pulse on against off), G3 (Redis blackholed
under load) and G4 under load.** Those need a running stack and come before the rollout.

## 8c. Gateway gate and stream (slice 5, partial)

- **Permission** `read_org_pulse` (`internal/rbac`): held by org admins and super admins,
  not by members, viewers or the empty legacy role (`TestPermissionMatrix`).
- **Routes:** `GET /v1/tenant/pulse/snapshot` and `GET /v1/tenant/pulse/stream` are proxied to
  `BNBLOC_PULSE_BASE_URL` (`TestPulseBoardRoutes`). The rest of `/v1/tenant/*` keeps `manage_org`.
- **Stream:** `text/event-stream`, a `snapshot` event now and every two seconds, an `error`
  event and close if a read fails (the board then shows disconnected). The stream test checks
  several events, the content type and that cancellation ends it.
- **Not built:** named executive grants (the permission is held by roles only), and the board
  itself is still the simulation.

## 8d. Load and chaos gates, measured (2026-10-25)

Stack: the repo's compose stack (`bnbloc-loadtest` project) on OrbStack, with an untracked
overlay (`loadtest.yml`, the trusted-proxy setting; `pulse.yml`, Redis and the consumer, and
the three emitting services pointed at the stream). Load: `cmd/bnbloc-loadtest`, 200 members,
20 rooms, 50 msg/s for 60 s, 10 s ramp.

| Gate | Result | Evidence |
| --- | --- | --- |
| **G2** (Pulse on vs off, paired) | **Inconclusive on this host.** No regression in the medians (send p50 5.6 vs 5.5 ms; p95 32.9 vs 21.9 ms; p99 128 vs 89 ms, on vs off). One on-run was an outlier (p50 14 ms, p95 163 ms). The host was overloaded (load average 20 to 60 on 12 cores; 47 GB of 48 GB in use, swapping; the user's IDEs and browsers). The run-to-run spread of identical arms is wider than the difference. **Not certified.** Rerun on a quiet machine before rollout. | `off-1..5.json`, `on-1..5.json` (not committed) |
| **G3** (Redis frozen mid-run, 30 s) | **Passed for the product.** 3,000 of 3,000 sends ok; send p95 11 ms; zero deliveries lost. **Pulse dropped events by design:** the emitter's counters show 2,077 `dropped_breaker` and 8 `dropped_sink` over the freeze, and the rollup was 1,700 short of what was sent. The accounting balances exactly (accepted = flushed + dropped). | realtime `/metrics`, `pulse_emit_total` |
| **G4** (consumer stopped 30 s, under load) | **Passed, shortened from 15 min.** 3,000 of 3,000 sends ok. After restart the rollup rose by exactly the 3,000 sent: no loss, no double count; stream lag 0, pending 0. | rollup delta |
| **G5** (correctness under load) | **Passed.** Messages sent with Pulse on: 17,000 (five on-runs and one probe). Rollup `messages`: 17,000. Exact. | `pulse_minutes` |
| Gateway gate, end to end | **Passed.** Super admin: 200 with real figures. Plain member: 403. Anonymous: 401. Stream delivers snapshot events through the gateway. | curl through the gateway |

Findings this run exposed, and what was done:

- The route 404'd because the pulse service served `/internal/v1/pulse/*` while the gateway
  forwards the public path. Fixed: the handlers now serve `/v1/tenant/pulse/*`.
- Emitter drops were invisible. Fixed: `pulse.RunMetrics` exports `bnbloc_pulse_emit_total`
  from activity, realtime and room.
- The repo's `.dockerignore` did not exclude `.claude/`, so image builds copied about 1.2 GB.
  Fixed.

Not run: the 15-minute consumer outage (G4 ran 30 s); G3 ran once.

Unrelated failure seen on the way: `core/protocol` `TestVMExecEnvNamesOnlyInProtocol` fails on
the committed `cmd/bnbloc-vmexec` (a constant spells an env name without `protocol.Env*`). It
predates this work and was not changed.

## 8e. Waiting jobs, weekly activity, and the agent path (2026-10-25)

- **Waiting jobs.** `job.queued` is emitted by realtime when a chat message queues an agent
  job. The consumer keeps a queued set per tenant: a job leaves it on `job.claimed`,
  `job.finished` or `job.swept`, and the window is the store's queued timeout
  (`store.DefaultQueuedTimeoutMs`, 120 s), so the sweep bounds the set. The snapshot carries
  `live.agentsWaiting` and `live.oldestWaitingSec`. Verified on the stack: a queued job rose to
  1 with its age ticking, then the sweep took it back at 120 s with `reason: no_bridge_online`.
- **Weekly active people** now counts members who sent a message, created a deliverable version,
  or wrote an app document (member writes only; an agent's own write names no member). The
  member is a keyed digest on each event, the same key in realtime and room (`BNBLOC_PULSE_KEY`).
  Verified: the deliverables test member moved the count from 200 to 201.
- **Provider family** is one function (`pulse.CoreFamily`), used by activity and realtime.
- **Agent path, verified once** with a real claude turn: bnbloc-acp (`agents.json`: claude via
  `@agentclientprotocol/claude-agent-acp`) on its own port, a bridge owned by the test member,
  and a chat message naming `core` and `provider`. The reply posted with usage; the board showed
  1 reply, 100% coverage, 13,694 tokens, about $0.01, a `claude-code` lane and one working tile.
  One turn, not a load: this spends the local account's model budget, so it is not a routine test.
- **Not done:** geography (globe pulses), app writes in the stack (the code path is in place, the
  test covers the schema, and no app writes were driven), and a production sign-in for the board.

## 9. What this plan does not do

- No database triggers, no CDC pipeline, no broker outside Redis.
- No synchronous call from any product path to Pulse, in either direction.
- No per-person rows in any Pulse table; presence is an HMAC'd, expiring set.
- No change to the existing notification outbox, the usage reports or the
  team dashboard. Those keep working unchanged.
