# Enterprise scale: load testing, realtime scale-out, retention, search

Status: approved 2026-10-10, in progress (sections 2 and 3 implemented 2026-10-11). Section 1 landed: `cmd/bnbloc-loadtest`, with the baseline in `docs/load-testing.md`.

## Problem

A large organization (thousands of members online, hundreds of active rooms)
hits four limits in the current design:

1. **No measured capacity.** Nobody knows how many streams, rooms or messages
   per second one deployment sustains.
2. **realtime and relay are singletons.** Room streams, per-room sequences,
   presence, the agent roster, plugin delivery and the relay pairing table
   live in one process. The Helm chart refuses replicas. A restart drops
   every stream (history is durable, so nothing is lost, but everyone
   reconnects), and capacity is one machine.
3. **Data only grows.** Messages, artifact versions, audit rows and blobs are
   never deleted. Organizations need retention to meet their compliance rules
   and to bound storage.
4. **Search is MySQL FULLTEXT (ngram).** Fine for small deployments, slow at
   hundreds of millions of messages.

What is already fine: gateway, room, activity and shareagent are stateless
and scale horizontally; background workers (notification outbox, schedules,
agent jobs) claim with leases, so several replicas are safe; blobs are
content-addressed in S3-compatible storage; agents run on members' machines,
so agent compute never lands on the server.

Out of scope: multi-region deployment (needs a concrete customer requirement
and touches every store).

## 1. Load test tool

`cmd/bnbloc-loadtest` drives a running gateway with simulated members:

- N members (dev sign-in, or API tokens from a file), R rooms, each member
  holding the room SSE stream the web client holds;
- a configurable chat send rate; each message carries a client id so the tool
  measures send → delivery latency on every receiving stream;
- reports p50/p95/p99 delivery latency, send errors by status, stream drops,
  and achieved throughput, as text and JSON.

`docs/load-testing.md` explains how to run it against the compose stack and a
cluster, and records the baseline measured on the compose stack.

## 1b. Write path

The section 1 baseline showed that delivery latency is the chat POST
latency, and that the POST waits on the MySQL commit, serialized per room:
each durable event held the room's publish lock across its own commit, so a
room's messages queued behind each other's fsync (p50 200 ms / p99 1.8 s at
50 msgs/s). Section 1b removes that wait without weakening any guarantee.

- **Per-room group commit.** Concurrent durable publishes to one room are
  committed together. The first publisher becomes the leader; publishes
  that arrive while a commit is in flight queue behind it. When the commit
  ends, the next queued publisher leads the next batch: it takes everything
  queued (bounded by count and bytes; no artificial wait), assigns
  consecutive sequences, and stores the batch in one multi-row insert
  through the store seam (`AppendRoomMessages`, memory store equivalent).
  Only after that commit succeeds does it deliver the batch in sequence
  order and hand each caller its own result.
- **Guarantees kept.** Per-room strict order; delivery only after durable;
  a failed commit consumes no sequence, delivers nothing and re-seeds the
  room's counter; `clientMsgId` idempotency, including two sends with the
  same id in one batch and a send whose id is already in history (both
  answer the original sequence); handoff import (`CommitHistory`) shares the
  room's queue and counter but is never delivered; transient events keep
  sequence 0 and never wait on a commit. The scale-out fence (the
  `(room_id, seq)` primary key) fails every member of the batch, so each
  caller's idempotent retry lands on the real owner.
- **Database client settings.** The pool keeps as many idle connections as
  it may open, so a load run does not churn connections, and the driver
  interpolates parameters client-side instead of paying a server-side
  prepare/execute/close per query, guarded by a utf8mb4-only connection
  charset check.
- **Shared SSE reader and session-token decoding.** The `bnbloc smoke`
  command, the bridge and the load tool each parsed the SSE stream; two
  places decoded session tokens. Both become one shared package each.
- **Re-measure** with the section 1 tool on the same compose baseline and
  record before/after numbers in `docs/load-testing.md`.

Status (2026-10-11): implemented; measured in `docs/load-testing.md`
("Write path").

- Database client settings: landed. The pool keeps `maxOpenConns` (20)
  idle. `interpolateParams` is on; enabling it exposed a semantic change the
  driver does not warn about: a `[]byte` argument is interpolated as a
  `_binary'…'` literal (the prepared protocol sent it as a connection-charset
  string), which MySQL refuses for JSON columns (error 3144). Every JSON
  column write now passes `json.RawMessage` (`store.jsonArg`); a type-checked
  scan of every `database/sql` call found no other `[]byte` argument. The DSN
  must be utf8mb4 (other charsets and collations are refused, and the
  negotiated session charset is checked at startup). No new env var: the
  defaults are right for every deployment we know of.
- Per-room group commit: landed (`internal/realtime/hub.go`,
  `store.ChatStore.AppendRoomMessages`). Decisions:
  - **Leadership hand-off, not a loop.** When a batch is delivered, the
    leader hands the lead to the first queued publisher and returns, so no
    caller's response waits on batches that arrived after its own.
  - **Batch bounds:** 64 events and 1 MiB of payload per insert.
  - **A commit runs to its real outcome.** It uses neither a caller's
    request context nor a timeout. The first version had a 10 s timeout:
    under a disk stall a commit timed out client-side, still committed
    server-side, its callers got 500 and its events were never delivered
    live, and the re-seeded counter then collided with the late rows (one
    run: 16 timeouts, then 359 fence failures). The pre-1b path had the
    same flaw through the request context (a client disconnect abandoned
    the insert). A dead connection still fails, through the driver and TCP
    keepalive.
  - **The fence fails every member, no local retry.** A `(room_id, seq)`
    conflict means another replica wrote the room's next slots. Re-seeding
    and retrying locally would keep a stale owner writing a room whose live
    readers are on the new owner; failing each caller sends its idempotent
    retry (by `clientMsgId`) to the real owner. Any other commit error also
    fails every member and re-seeds.
  - **History duplicates retry the rest.** Rows whose client id is already
    stored answer their stored sequence; the rest of the batch is
    renumbered from the same base and retried, so sequences stay dense.
    The store classifies the conflict by data (one lookup after the failed
    insert), not by index name.
  - **Revocations queue, other transients do not.** `member_removed` and
    `room_deleted` queue behind in-flight durable events, so a message
    committed before a revocation is never delivered after it (and never
    replays into a reused room id); presence, typing, roster and job deltas
    are delivered immediately.
  - **Per-row tenant.** Each batch row carries the tenant its publisher
    asserted (`RoomMessage.TenantID`), so batching never changes which
    tenant a row is stored under.
  - **Metrics:** `realtime_feed_commits_total{result}` and
    `realtime_feed_commit_events_total` (mean batch size).
- Overflowing streams: landed. The first re-measurement showed lost
  deliveries with no disconnects: a batch reaches each subscriber as one
  burst, and the hub skipped (silently) any subscriber whose 64-event
  buffer was full. That skip predates group commit (a slow client could
  always miss events without noticing); bursts made it visible. A full
  subscriber is now closed, so the client reconnects and catches up from
  history (`realtime_sse_overflows_total` counts it), and the SSE handler
  writes every queued event before one flush instead of flushing per event.
- Shared SSE reader and session-token peek: landed. `internal/sse` is the
  one text/event-stream parser (comments, `id:`/`event:`/`data:`,
  multi-line data, blank-line dispatch, a 4 MiB line bound); the bridge,
  `bnbloc smoke` and the load tool read through it and keep their own
  reconnect logic. The bridge used to dispatch on every `data:` line with a
  1 MiB line cap, below the largest event realtime may emit (a 1 MiB
  internal publish plus its envelope); it now dispatches on the blank line
  like the others. `auth.PeekSession` (beside the signer that owns the
  format) replaces the two copies of unverified session decoding.
- Re-measurement: done, on a host heavily loaded by unrelated workloads
  (load average 60 to 330, disk stalls of 1 to 15 s), so the tails are
  noise-dominated and recorded as such. Medians fell in every shape (paired
  runs: A 26 to 18 ms, one room at 60 msgs/s 26 to 11 ms; heavy load: 85 to
  835 ms down to 10 to 42 ms), p99 did not measurably change (it is the
  disk stall), prepared statements fell to zero, and new connections per
  run fell 3 to 15 times. Under slow commits one busy room saturated the
  old build and not the new one. The measurement also found the two
  defects fixed above (silent drops on a full subscriber, the commit
  timeout).
- Replay buffer max age: landed (raised by section 3). The replay buffer is
  only a latency optimization for connected clients, yet a quiet room whose
  subscriber stayed connected kept its entries forever, so a message data
  retention deleted could replay to a new subscriber. Entries now carry the
  hub time they were retained at (never the client-settable `atUnixMs`) and
  expire after one day, dropped on subscribe and on publish. One day loses
  nothing (every client pages history on connect) and is far below the
  31-day retention minimum; a test pins that relation.

## 2. realtime and relay scale-out by key ownership

Each room is owned by exactly one realtime replica, so ordering, per-room
sequences, presence and the roster stay in-process exactly as today. No new
infrastructure (no Redis or NATS).

- **Peer set.** `BNBLOC_REALTIME_PEERS` is either a DNS name resolved
  periodically (a Kubernetes headless Service) or a comma list of
  `host:port`. `BNBLOC_REALTIME_SELF` is this replica's own address (pod IP
  through the downward API). Both unset = single node, today's behavior with
  no forwarding. relay gets the same pair (`BNBLOC_RELAY_PEERS`,
  `BNBLOC_RELAY_SELF`).
- **Ownership.** Rendezvous hashing of the key over the sorted peer set. One
  shared package (`internal/shard`) serves realtime, relay and the gateway.
- **Forwarding.** A replica that receives a request for a key it does not own
  proxies it to the owner (one hop; SSE streams are proxied with flushing).
  Callers (gateway, activity, room, bridge) are unchanged: they keep calling
  the load-balanced Service. A forwarded request is marked so it is never
  forwarded twice. A replica that receives a forwarded request it does not
  own (the peer views disagree for a moment) serves it locally instead of
  looping.
  - Room-scoped routes (`/v1/rooms/{roomId}/…`, `/internal/v1/rooms/{roomId}/…`)
    use the room id as the key.
  - Member streams (`/v1/my/plugin-events` and its ack) use the member.
    Plugin and notification deliveries produced by a room owner are handed to
    the member's owner over an internal route.
  - relay keys on the session key.
  - Read-only, store-backed routes (`/v1/rooms-unread`, blobs) are served by
    any replica. More than one realtime replica requires the S3 blob backend;
    startup refuses the disk backend with peers configured.
- **Ownership change.** When the peer set changes, a replica closes the
  streams of keys it no longer owns. Clients reconnect, land on the new owner
  and catch up from history (the existing reconnect path). The new owner
  seeds the room's counter from the durable max (`Hub.SeedSeq`). The durable
  `(room_id, seq)` primary key fences two replicas that briefly both believe
  they own a room: the loser's commit fails and the client's retry
  (idempotent by `clientMsgId`) lands on the new owner. Sequence numbers that
  transient events used must never make a client drop a later durable event;
  the implementation verifies the client and bridge dedup rules and fixes the
  root cause if they can.
- **Deploy.** The Helm chart runs realtime and relay as StatefulSets behind
  a headless Service with `replicas` allowed above 1, and drops the singleton
  guard. compose stays single-node. Every deploy surface and the env docs
  change together.

### Gateway rate limiting with replicas

The per-subject token bucket lives in each gateway replica's memory, so N
replicas allow N times the configured rate. The gateway reads the same kind of
peer set (`BNBLOC_GATEWAY_PEERS`) and divides the configured rate and burst by
the live replica count. This is the standard local-rate-limit approximation;
it needs no shared store.

### Status of section 2 (2026-10-11): implemented

`internal/shard` (peer set, rendezvous ownership, one-hop `Forward`),
realtime (`services/realtime/scaleout.go`), relay, the gateway limiter, the
Helm chart (StatefulSets + headless `bnbloc-<name>-peers` Services,
singleton guard removed), the all-in-one image and the env docs. Decisions
taken while implementing, beyond the text above:

- **Liveness, not just membership.** Every candidate of the peer set (DNS
  answer or fixed list) is probed with `GET /readyz` on each 5 s refresh;
  two consecutive failures drop it, one success restores it. So a dead
  replica leaves the view in list mode too, an unready one owns no keys, and
  the gateway's divisor is the live count. A replica judges itself with its
  own readiness check (no network self-probe). A failed DNS lookup keeps the
  previous view; an empty view serves locally.
- **`BNBLOC_<SVC>_SELF` may be a bare IP** in DNS mode (it takes the DNS
  entry's port), so the chart passes the pod IP unchanged and IPv6 works.
- **The gateway reads only `BNBLOC_GATEWAY_PEERS`** (it owns no keys); it
  divides all three per-replica limits (chat/borrow per subject, guess,
  public share reads), never below one token of burst.
- **Release on every refresh, not only on change.** A replica may serve a
  forwarded request locally while views disagree; the next refresh closes
  any such stream so the client moves to the real owner instead of missing
  the room's events. A dead owner answers 502 for at most two refreshes;
  clients retry (SSE reconnect backoff, idempotent send).
- **Cross-replica work.** The room owner hands each hook-bearing event to
  every peer (bounded per-peer queues; each peer fans out to the member
  streams it owns — this is "handed to the member's owner" without the room
  owner having to know which members hold streams). The notification worker
  (lease-claimed on every replica) asks the member's owner whether a desktop
  is connected and delivers there, waiting for the owner's outcome; an
  unreachable owner falls back to the server notifiers. A schedule slot
  claimed on any replica is posted by the room's owner over an internal
  route, with its pause/retry classification preserved.
- **The ownership marker** (`X-Bnbloc-Forwarded-By`) is stripped by the
  gateway; relay's routes bypass the gateway, where a client-set marker only
  makes its own request unpairable.
- **The chart scales on values.** Peer variables are set only when
  `replicas > 1` or an HPA is enabled; more than one realtime replica
  without an S3 bucket fails the render (the binary refuses it too).

**Transient sequences (verified, root cause fixed).** Transient events
(presence, typing, roster, job/state deltas) took the room's next sequence
but were never persisted, so a restarted or new owner seeding from the
durable max re-issued numbers clients had already seen. Checked consumers:
the web feed (`applyIncoming`, `mergeHistory`, `maxSequence`, read cursor)
dedupes and orders only durable rows; transient kinds are handled by
payload, never by sequence; `EventSource` is recreated on every reconnect
(no `Last-Event-ID`); the bridge never dedupes by sequence and reads history
by its own durable cursor. So no durable event was dropped — not a live bug.
The design was still wrong for ownership moves: the hub doc promised
"consumers ignore older deliveries", a transient publish needed a store seed
(a DB blip dropped presence), and transient publishes on a room that had
moved away left a stale counter that collided with the new owner's commits
when the room moved back. Fix: only durable kinds are sequenced; transient
kinds carry sequence 0 and no SSE `id:`, delivered in order under the same
room lock. The counter now always equals the durable max.

**Process-local state and how it behaves under ownership.**

| State | Key | Behavior on an ownership move |
| --- | --- | --- |
| Hub subscribers (room SSE streams) | room | Closed on the old owner; clients reconnect to the new owner and catch up from history. |
| Durable sequence counter | room | Forgotten on release; the new owner seeds from the durable max. Two replicas briefly owning a room are fenced by the `(room_id, seq)` primary key: the loser's commit fails, it re-seeds, and the client's retry (idempotent by `clientMsgId`) succeeds. |
| Replay buffer | room | Forgotten on release (it only serves connected subscribers); reconnecting clients page history. |
| Presence refcounts + leave timers | room | Closing streams run their deferred leave on the old owner, whose events are suppressed (only the owner announces presence); the new owner rebuilds presence from the reconnects. |
| Agent roster + withdraw timers | room | Same as presence; each bridge re-advertises when its stream reconnects. |
| Room group commit queue (section 1b) | room | Per replica; only the owner publishes, so its one leader at a time serializes the room's sequence. |
| Member plugin streams | member | Closed on the old owner; the desktop reconnects to the new owner. |
| Plugin pending-ack table | member (delivery id) | Deliveries sent before a move stay pending on the old owner; their acks now route to the new owner (404), so they expire after 60 s, audited failed; a notification then falls back to the server notifiers. |
| Plugin fan-out queue, per-peer hand-off queues | — | Bounded; full queues drop (logged), as before. |
| Plugin record snapshot, per-stream install cache, notifier endpoints, secret cache | — | Per-replica caches of durable records (TTL / reconcile); no ownership. |
| Notification observer (reply ↔ job matching) and collapse window | room | Run where the room's events are published (the owner). Parked state on the old owner flushes without a summary after its wait; a burst straddling a move may notify twice. |
| Notification outbox worker, scheduler | — | Durable lease claims on every replica; desktop routing and posts go to the owner. |
| Rate limits | — | realtime has none in process (size caps only); the gateway divides its buckets by the live replica count. |
| Relay rendezvous queues | session | Dropped on the old owner; peers re-exchange on the new owner. |
| Relay parked first peer | session | Closed so it reconnects to the new owner. |
| Relay established pipe | session | Left alone: both halves are already on one replica. |

**Real-runtime check (2026-10-11, one developer machine).** Native binaries
of room, gateway and three realtime replicas (fixed peer list
`127.0.0.1:18182,…:18282,…:18382`, S3 settings so the disk refusal does not
trip) against one MySQL 8.4 container, with a small round-robin proxy that
skips refused backends standing in for the Kubernetes Service. Startup
refusals were checked first (peers + disk store; peers without self). A
harness held 12 room streams through the gateway the way the web client
does (reconnect with backoff, catch up from history on every connect,
dedupe by sequence) while six members sent chat at 1 msg/s each with
`clientMsgId`s, retrying failures. One replica got `SIGKILL` 22 s in and
was restarted 20 s later. Result: 322 sends acked, 0 lost, 0 duplicated,
0 out of order, every stream converged to history. The dead owner left the
other views after two failed probes (8 s); until then its rooms answered
502 and senders retried. On restart the returning replica owned its rooms
2 s before the others noticed: one commit in that window hit the
`(room_id, seq)` fence (500), re-seeded, and the client's retry landed;
then the old owners closed 6 streams, which reconnected to it.

The section 1 load tool, run A shape (200 members, 20 rooms of 10,
50 msgs/s, 45 s) on the same native stack: three replicas delivered 22,500
of 22,500 (p50/p95/p99 210/702/950 ms, stream connect p50 77 ms); one
replica delivered 22,500 of 22,500 (p50/p95/p99 221/1729/2555 ms, connect
p50 57 ms). Both are bound by the MySQL commit on this disk (see
`docs/load-testing.md`), so the tail difference is noise-prone; the
forward adds about 20 ms to opening a stream and nothing measurable to
delivery.

## 3. Data retention

- **Policy.** A platform default (super-admin) and a per-tenant override
  (tenant admin), in days, for:
  - room messages;
  - artifact versions other than the latest;
  - the audit log;
  - finished agent jobs.

  Unset means keep forever (today's behavior).
- **Worker.** Deletes in small batches by age. It is safe to run on several
  replicas, because concurrent batch deletes of the same expired rows are
  harmless. It reports deleted counts as metrics.
- **Blobs.** After rows are deleted, a garbage-collection pass removes blobs
  that no remaining row references (attachments, artifact versions, sites).
  A grace period protects blobs uploaded but not yet referenced.
- **UI.** The settings live where tenant and platform settings already live.
  The UI states plainly what is deleted and when.

### Status of section 3 (2026-10-11): implemented

`internal/wire/retention.go` (policy, limits, effective resolution),
`internal/store/retention.go` + `mysql_retention.go` (purge per kind, blob
references, the retention lock), `internal/retention` (the worker and the
blob collection), the shareagent routes, realtime wiring, the web settings
(Organization → Settings, Platform console overview) and
`docs/production-deployment.md#data-retention`. Decisions taken while
implementing:

- **Where the policy lives.** The platform default is the platform setting
  `retention.default` (beside the platform-agent policy); the tenant
  override is four nullable `tenants.retention_*_days` columns. shareagent
  owns both (it owns the tenant registry and the platform policies):
  `GET/PUT /v1/platform/retention` (manage_platform) and
  `GET/PUT /v1/tenant/retention` (manage_org; the gateway binds the
  tenant). In the default null keeps forever; in an override null uses the
  default and 0 keeps forever, so an organization can opt out of a platform
  default. Days are 31–36,500: monthly budgets and borrow reports read the
  current month's agent replies and audit rows. Every change is audited
  (kind `retention`, action `policy`), and every pass that deletes writes a
  `retention` / `purge` entry per tenant and kind.
- **What each kind covers.** Messages: `room_messages` with their message
  tasks, artifact anchors (`source_seq` cleared), the activity copy of agent
  replies (`agent_messages`, except content-free submit-provenance rows),
  and attachments by upload age (the message is sent right after its upload;
  an attachment id re-sent in a newer message is not tracked). Read cursors
  need no cleanup (unread counts only seq > cursor), history pages by
  sequence and tolerates gaps, and the FULLTEXT index follows the rows.
  Artifact versions: every version but the artifact's latest, with the
  share links pinned to it. Audit: `shared_agent_audit` and
  `extension_audit` per tenant; platform rows (tenant "") follow the
  default. Agent jobs: terminal jobs only.
- **The max-sequence invariant (root cause, not a guard).** The hub seeds a
  room's counter from `MAX(seq)`; deleting a room's newest rows would lower
  it and re-issue sequences clients and read cursors already passed. Of the
  two root fixes — never delete the max-seq row, or persist the counter
  apart from the rows — this keeps the max-seq row, because the invariant
  then holds by construction for every reader (`MaxRoomSeq`, the scale-out
  fence on `(room_id, seq)`, the group commit) without touching the write
  path. The row is not kept as content: it becomes a fence (kind
  `retention_fence`, empty payload, no author, text, client id or edit
  stamp, soft-deleted), so it keeps only room, sequence, channel and time;
  every reader already skips soft-deleted rows. A later pass deletes a fence
  once the room has a newer row. Tested on memory and MySQL.
- **Cheap deletes.** Each kind walks an index on (tenant, age) in batches of
  500, verified with `EXPLAIN` on MySQL 8.4 with seeded data: room messages
  use a new `(tenant_id, kind, created_at_ms)` index (one range per kind,
  kinds found by an index dive each, so fences are never re-read) and the
  primary key for the per-room max and the deletes; attachments and
  artifact versions get `(tenant_id, created_at_ms)`; audit, agent replies
  and jobs reuse existing indexes. Artifact versions page with a keyset
  cursor so the latest versions they skip are read once per pass. New
  indexes on `attachments.blob_key`, `artifact_versions.blob_key` and
  `.thumb_blob_key` make the reference check a lookup.
- **Several replicas.** Every realtime replica runs the worker; a MySQL
  advisory lock (`GET_LOCK('bnbloc_retention', 0)` on a pinned connection)
  lets one pass at a time run, the others count `skipped`. Concurrent
  passes would also be correct. The worker only deletes rows and blobs and
  never touches a room hub, so it needs no room ownership.
- **Blob collection.** At most daily (status in the platform setting
  `retention.status`), after the row pass: walk the blob store (`Walk`:
  ListObjectsV2 on S3, the shard directories on disk; `Stat`: HEAD /
  `os.Stat`), skip blobs stored within the 7-day grace period, ask the store
  which of each batch of 500 keys a row references (attachments, artifact
  versions and thumbnails, and any `sha256/<hex>` a platform setting
  mentions: component packages, desktop releases, the update config — one
  function lists every source), then re-`Stat` each unreferenced candidate
  and delete it only if it is still older than the grace period. Content
  addressing means a blob backing several rows goes only with the last
  reference; a `Put` of existing content rewrites the blob (fresh time) on
  both backends, so a re-upload racing the collection is kept. The residual
  window is between that `Stat` and the delete, for bytes that lay
  unreferenced longer than the grace period. The unused
  `AttachmentStore.DeleteRoomAttachments`, which handed blob keys to a
  caller to delete (unsafe with shared blobs), is removed.
- **Replay buffer (resolved in section 1b).** The hub's in-memory replay
  buffer (the last 256 events of a room with subscribers) could still hold
  a message retention deleted, in a room that kept a subscriber connected
  and had no traffic for the whole period. Replay entries now expire after
  one day (`replayMaxAge`, far below the 31-day retention minimum), dropped
  on subscribe and on publish.

## 4. Search backend

- `internal/search` defines the index interface. MySQL FULLTEXT stays the
  default implementation.
- An OpenSearch implementation is selected by `BNBLOC_SEARCH_OPENSEARCH_URL`
  (plus credentials). It talks plain REST with no SDK dependency. The index
  is kept current on publish, edit, delete and retention deletes; a failure
  is logged and counted as a metric and never fails the chat write.
- `bnbloc search reindex` backfills or rebuilds the index from MySQL.
- Search results always pass the same membership and people-chat filters as
  today; OpenSearch returns candidates and the store stays the authority.

## Order

1 and 2 first (2 is measured with 1), then 3, then 4. Each lands with its own
tests, deploy surfaces and docs, and updates this file's status.

## Verification

- The load test runs before and after scale-out. The scale-out run uses 3
  realtime replicas and reports latency and throughput.
- A replica killed mid-test: clients reconnect, no durable message is lost or
  duplicated, and no client drops a later message.
- Retention: rows older than the policy are gone, newer ones untouched, and
  unreferenced blobs are collected.
- Search: the same queries return the same visible results on MySQL and on
  OpenSearch, including the people-chat and membership filters.
