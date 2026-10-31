# Replicas without owners: a simpler multi-replica realtime (2026-10-30)

Status: approved (2026-10-30: "这个方案可以啊", with "if there is a better
one, dig further": §6 is that dig). Building: slices in §5. The owner: "有没有更简单更
灵活高性能高可用的多副本部署方案？". Builds on
`docs/prd/2026-10-30-high-availability.md` (what it took to make today's
design survive a failover) and `docs/prd/2026-10-10-enterprise-scale.md` §2
(scale-out by key ownership).

## 1. The question, and what today's design costs

Every bnbloc service but realtime and relay is already interchangeable:
any replica serves any request, state is in MySQL. realtime is different:
each room is **owned** by one replica (rendezvous hashing over a peer set
found by DNS and probes), which holds the room's subscribers, sequence
counter, replay buffer, presence and roster; every other replica forwards
the room's requests and streams to it.

What owning costs, measured this week:

- **Moving parts.** Peer discovery (DNS every 5 s, probes every second),
  one-hop forwarding with a retry when the owner died, release of rooms on
  every view change, the sequence fence, the hand-over watch that delivers
  what a previous owner stored, forced gap reads while a room changes hands,
  `Leave` on drain. About 1,500 lines (`internal/shard`,
  `services/realtime/scaleout.go`, `handover.go`), and every one of them was
  needed to stop a failover from losing messages
  (`docs/bugs/2026-10-30-owner-change-loses-messages.md`).
- **Windows of two owners.** A replica joining is seen by the others up to
  5 s apart; for those seconds two replicas own a room. Every correctness
  fix since is a way to repair what happens in that window.
- **Reconnects.** A view change moves rooms, so their streams reconnect —
  also when a replica *joins*. In the failover drill (300 streams, 4 events:
  drain, rejoin, crash, rejoin) streams reconnected 564 to 884 times. Only
  the streams of the replica that went away had to: about 200.
- **Every deploy is 2 moves per pod.** A rolling update stops a pod (its
  rooms move away) and starts its replacement (other rooms move to it): most
  clients reconnect twice per deploy.
- **An extra hop for most traffic.** With N replicas, (N-1)/N of posts and
  stream opens land on a replica that does not own the room and are proxied
  on; a forwarded stream's bytes pass through two pods for its whole life.
- **No autoscaling in practice.** The chart's own comment: every scale event
  moves a share of rooms; "prefer a fixed count".

## 2. How others do it

| System | Who holds a conversation's live state | How replicas share a message | Catching up |
|---|---|---|---|
| Discord | one guild process (owner); relays split its fan-out | Erlang distribution | sessions resume by sequence |
| Slack | one Channel Server per channel (consistent hashing, CHARMs replace a dead one in < 20 s) | Gateway Servers subscribe to Channel Servers | — |
| Telegram | a message box per channel with its own `pts` | own servers | `getChannelDifference` from the client's `pts` |
| Centrifugo | **nobody**: any node serves any client | broker PUB/SUB (Redis, NATS, Postgres; memory for one node) | a bounded per-channel history in the broker, `epoch` + `offset`, "never a partial recovery"; the database stays the truth |
| Socket.IO (Redis adapter) | nobody | Redis PUB/SUB | the application's |

Sources: the engineering posts cited in
`docs/prd/2026-10-30-message-path-performance.md` §2; Centrifugo's
documentation ("Stream history and recovery", "Engines"); read 2026-10-30.

Discord and Slack own conversations because a guild of a million online
members needs its member list and permissions in one process and its fan-out
split across machines. Their owners are replaced by dedicated coordinators
(Slack's CHARMs) or handed over with their state (Discord's guild hand-off).
An enterprise room of hundreds to a few thousand people needs neither. The
systems built to be deployed by others, at any scale from one node up, chose
the other way: **no owners, a broker between interchangeable nodes, the
database as the truth, a short history to catch up from**. Centrifugo says
why the history matters: on a deploy everyone reconnects at once, and
without it every reconnect queries the database.

## 3. The proposal: realtime as interchangeable replicas over a bus

Every realtime replica serves any room. A message is committed by whichever
replica the request reached, then published on a **bus**; every replica with
a stream of that room receives it and fans it out to its own streams.

| Today (owner) | Proposed (no owner) |
|---|---|
| room sequence: the owner's in-memory counter, seeded from the durable max, fenced by the `(room, seq)` key | a `room_counters` row updated in the message's own transaction (`UPDATE … SET seq = seq + n`): its row lock orders a room's batches across replicas, as the owner did, and commit order is sequence order |
| live delivery: the owner's hub | publish after commit on the bus channel of the room; each replica subscribes to the rooms it has streams for |
| order | per room by sequence; a replica holds an event a moment (≤ 300 ms) when the one before it has not arrived, as Telegram waits 0.5 s on a gap |
| resume | `since` against history in MySQL (`RoomFeed` after the client's sequence: one indexed read per reconnect); later, if a deploy's reconnects load the database, a short per-room history in the bus (Centrifugo's design) |
| presence, agent roster | entries in the bus's store with a TTL, refreshed by the replica holding the stream; deltas on the bus |
| room state (channels, sections, members) | each replica's own cache, dropped by the same room events, which now reach every replica on the bus |
| slow mode | an atomic key with the gap as TTL (`SET NX PX`) |
| a member's desktop plugin stream | a bus channel per member |
| scheduled posts, workers | leases (already) |

The **bus** is one small interface with two implementations:

- **in process** — one replica, no dependency: development, the all-in-one
  image, air-gapped installs (as today);
- **Redis** — PUB/SUB (sharded PUB/SUB on a Redis Cluster), TTL keys for
  presence and slow mode. ElastiCache Multi-AZ on AWS; pulse already uses a
  Redis for its stream.

Measured: Redis PUB/SUB, 3 subscriber replicas, 100 rooms, 500 msgs/s of
400 bytes, on this laptop under a load average of 185: **p50 0.20 ms, p95
0.52 ms, p99 1.0 ms**, 15,000 of 15,000 received. Against a chat post of
5 ms (p50), the bus is noise.

What it removes: peer discovery and forwarding for realtime, the sequence
seed and fence, release on view change, the hand-over watch and the forced
gap reads, `Leave`. What stays: drain (a stopping replica's own streams
reopen elsewhere, once), `since` and `stream_gap` (a replica that missed bus
messages — PUB/SUB is at most once — tells its streams to read the
difference), the client's resend.

### What a deployment becomes

| Part | Replicas | Notes |
|---|---|---|
| gateway, room, activity, shareagent | 2+, HPA | as today |
| realtime | 2+, a Deployment, HPA | no peer Service, no StatefulSet; a pod going away reconnects only its own streams |
| relay | 2, keyed | P2P sync pairing needs both peers on one replica: keep its ownership, or route by session at the ingress (consistent hash); off the message path |
| pulse | 1 | unchanged |
| MySQL | Aurora/RDS Multi-AZ, behind RDS Proxy | the proxy shortens a writer failover for clients and pools every pod's connections |
| Redis | ElastiCache Multi-AZ | bus, presence, slow mode, pulse |

And because every service is then interchangeable, the **all-in-one image
can run as N identical copies** behind a load balancer with MySQL and Redis:
a small organization gets high availability from one image.

### Failure modes

- **Redis fails over** (seconds): publishes fail and are retried; messages
  are still committed; each replica tells its streams `stream_gap` when its
  bus connection returns, and clients read the difference. Nothing is lost;
  live delivery pauses for the failover.
- **A replica dies**: its streams reopen on the others (once) and resume by
  sequence; posts in flight are resent by the client. No room moves.
- **A replica is slow on the bus** (a full subscription buffer): it drops
  and tells its streams `stream_gap` (as the hub's overflow does now).

### Costs and risks

- realtime's hub is the core of the product; the change touches ordering,
  idempotency (the batch insert's client-id conflicts stay), presence and
  plugin delivery. Every guarantee of the 1b write path has a test today;
  they all carry over.
- One more dependency for multi-replica realtime (Redis). Single-node
  deployments keep none.
- A replica receives every message of every room it has a stream in, so a
  room whose members are spread over N replicas costs N bus deliveries per
  message (cheap: 0.2 ms each, measured).

## 4. Alternatives considered

- **Keep owners, coordinate better** (leases in MySQL or Kubernetes `Lease`
  objects per room, or endpoints watched through the Kubernetes API instead
  of DNS): shrinks the two-owner window but keeps forwarding, moves on join
  and the double reconnect per deploy. More code, not less.
- **Active/standby** (one realtime, a hot standby taking over by lease):
  simplest, no scale-out; every client reconnects on every failover and
  deploy.
- **Adopt Centrifugo** as the realtime tier: its model is the proposal's,
  but our per-event access rule (members-only sections), lanes and
  activity, agent admission and plugin hooks would have to live in proxies
  around it; two systems to run instead of one.
- **NATS JetStream** as the bus: per-subject ordered history with replay by
  sequence is a better bus than Redis PUB/SUB, but a new stateful system for
  operators; Redis is already in the stack. The interface keeps it possible.
- **The gateway as the fan-out tier** (it terminates the stream itself and
  subscribes to the bus, like Slack's Gateway Servers): saves one more hop;
  moves the access rule into the edge. Later, if the hop shows.

## 5. Plan (slices)

1. `room_counters` and the commit path through it (single node too); the
   fence and the seed go.
2. The bus interface and the in-process bus; the hub delivers what it
   receives from the bus (its own publishes included); the reorder hold.
3. The Redis bus: subscribe a room on its first local stream, unsubscribe a
   while after its last; `stream_gap` after a bus reconnect.
4. Presence, roster and slow mode on the bus's store; member channels for
   plugin delivery.
5. Remove realtime's ownership (forwarding, release, hand-over, peers);
   realtime as a Deployment with an HPA in the chart; relay keeps its own.
6. Drills: `scripts/failover-check.sh` (pass: no send failed, no delivery
   lost, about one reconnect per stream of the replica that went), a rolling
   deploy under load, a scale-out from 2 to 4 under load, a Redis failover
   under load.
7. (B) One stream per client: the stream's open set spans rooms; unread
   state on open, deltas after; the shell's polling goes; presence per
   member.
8. A longer resend for a 503 from the database; RDS Proxy in the deployment
   guide.

## 6. Digging further: is there something better?

Each idea below was weighed against the proposal on the four words of the
question: simpler, more flexible, faster, more available.

**Better, and added: one stream per client, not per room (part B).** The
web client today opens one room stream per open room and, besides it,
polls every room's unread counts every 25 s (`web/src/views/Shell.tsx`). One
poll asks `UnreadCount` per readable channel of every room, two queries
each (`services/realtime/service.go` `handleUnread`): a member in 20 rooms
of 5 channels costs 200 queries a poll; 1,024 clients cost about **8,000
queries a second of polling**, far more than every message they send
together. Slack and Discord hold **one connection per client** that
carries every conversation: the initial state once, then deltas. With
lanes and the bus that is a short step:

- a client's stream carries full events for what it has open (a channel and
  a thread, of any room) and one coalesced `channel_activity` for every
  other channel of every room it may read;
- on open the stream sends the unread state once (one batched query), then
  only deltas; the polling goes;
- opening another room re-scopes the stream as opening another channel does
  now: no reconnect, no replay;
- the replica holding the stream subscribes the bus to the member's rooms
  (and the member's own channel: desktop plugin deliveries, notifications);
- presence becomes a member's, not a room stream's.

**Better, and added: subscribe, then read the difference.** A stream
subscribes to the bus *before* it reads the difference from MySQL, and
merges both by sequence: a message committed between the two is then in
one of them, never in neither. The read goes to the primary (a replica's
lag could hide the newest rows the subscription has not seen either).

**Better, and added: a longer resend for a database failover.** Aurora's
writer failover takes tens of seconds; the client's resend (400 ms, 1.2 s,
3 s) covers a replica, not a database. Answers that name the database as
unavailable (503) are resent with backoff up to 30 s, and RDS Proxy in front
of the writer keeps connections through it.

**Considered, not better:**

- **MySQL as the bus** (each replica tails `room_messages` by a global id
  every 50-100 ms): no new dependency, but adds the poll interval to every
  delivery, and auto-increment ids are not commit order (a reader can pass
  an id that commits later). And transient events (typing, a job's
  progress, live frames, presence) do not belong in the database.
- **A peer mesh of our own** (replicas stream events to the peers that
  asked for a room, over the existing signed peer HTTP): no Redis, but we
  would write and maintain a pub/sub (interest, backpressure, presence
  gossip) that Redis gives. Kept possible behind the bus interface for
  deployments without Redis; not first.
- **Embedded NATS** (a NATS server inside each realtime replica, clustered
  through the peer Service): a real pub/sub with interest routing and no
  external service, but a large dependency for a module that keeps its
  dependencies small, and a cluster hidden inside application pods.
- **The gateway as the fan-out tier**: one hop fewer, but the access rule
  and lanes would move into the public edge.
- **One binary for every service**: the all-in-one image already ships them
  together; once realtime has no owners, N copies of it behind a load
  balancer are highly available. Merging processes saves loopback hops only.
- **Cells** (a full stack per group of tenants, Slack's cellular
  architecture for zone failures): the end-state of a multi-tenant service
  at a large scale; one enterprise deployment is one cell.

So the target is **A** (interchangeable realtime replicas over a bus, the
sequence in MySQL) then **B** (one stream per client). Slices of B follow
A's in §5.

## 7. Decisions pending owner

- **R1.** Build it: realtime without owners, Redis as the multi-replica bus
  (single node stays dependency-free)? Recommended.
- **R2.** Resume from MySQL first, and add a short history in Redis only if
  a deploy's reconnects show on the database? Recommended.
- **R3.** QA: is there an ElastiCache Redis for bnbloc (pulse's), or should
  one be created (Multi-AZ, small)?

## 8. Progress

- **Slice 1 (2026-10-30): the store numbers a room's messages.**
  `ChatStore.AppendNextRoomMessages` advances the room's `room_counters`
  row (`UPDATE … SET seq = LAST_INSERT_ID(seq + n)`) and inserts the batch in
  one transaction; a room with no counter starts it at its stored max, in a
  statement of its own (inside the transactions, concurrent first writers
  deadlocked: found by the contract test on MySQL 8.4); a counter behind
  rows written at chosen sequences catches up once; a deadlock victim runs
  again. The hub's seed (`Hub.SeedSeq`), its counter (kept only for
  in-memory mode with no journal) and the fence handling are gone. Tests:
  the store contract (dense, in order, conflicts consume nothing, 8
  concurrent writers of one room on both backends), the hub's group commit
  with another writer committing into the room meanwhile.
  The failover drill (`scripts/failover-check.sh`, 3 replicas, drain,
  rejoin, crash, rejoin) passes on it: 6,000 of 6,000 sends, 19,441 of
  19,441 deliveries, no counter deadlock or duplicate logged.
- **Slices 2 and 3 (2026-10-30): the bus.** `realtime.Bus` (`bus.go`): an
  endpoint per replica; what one publishes reaches the room's other
  subscribers, never itself (a replica delivers its own events directly);
  at most once, with `lost` when a subscription may have missed events.
  `MemoryBus` connects hubs in one process (tests); `redisbus` is Redis
  PUB/SUB, one channel per room, one subscriber connection per replica,
  enabled by `BNBLOC_REALTIME_BUS_REDIS_URL` (on in `compose.ha.yml`).
  The hub (`hub_bus.go`): a room is received from the bus while it has a
  stream here and 30 s after the last; joining subscribes first, then reads
  the room's head (`RoomHead`, the stored max), so a message committed
  meanwhile is in history or on the bus, never neither, and the replay
  buffer starts again (a stream joining then reads the difference). Durable
  events, its own and others', are delivered in sequence order: a later one
  waits at most 300 ms for a missing one, then is delivered and the room's
  streams read the difference; one already delivered is dropped. A lost
  subscription (Redis reconnect) gaps the room's streams and restarts the
  order at the head. Other replicas' events reach this replica's room state
  (`Hub.Received`): a kind that changes it drops the room's state under
  every tenant, since bus events carry none.
  Not yet: ownership still routes posts and streams to one replica (slice
  5 removes it); until then the bus covers the moments of two owners.
  Known limit, for slice 5: a revocation from one replica and a message
  from another are not ordered with each other (a removed member's stream
  can see a message committed just after the removal, for the bus's
  milliseconds); and a replica must be on the bus for every room it caches
  state of, not only the rooms it streams.
  The failover drill with the bus on passes: 6,000 of 6,000 sends, 19,313
  of 19,313 deliveries; across the three replicas the bus carried 3,874
  publishes and 1,056 receipts (the rooms' moments of two owners and their
  linger), dropped 42 duplicates (the hand-over watch and the bus both
  bringing one), held none, lost none.
- **Slice 4, first part (2026-10-30): presence, the roster and slow mode
  across replicas.** `realtime.RoomViews` joins each replica's view of a
  room (who is here on its streams, which agents its bridge streams serve)
  into the room's; each replica tells its own streams what changed in the
  room's (`presence_delta`, `agent_roster`, now delivered locally:
  `Hub.PublishLocal`), so a member on two replicas is here until gone from
  both. Views go on the bus whole (`room_view`, an internal kind,
  `Hub.Announce`) on every change and every 15 s; a replica that starts
  receiving a room asks for the others' (`Hub.Joined` → `Query`); a view
  not heard for 45 s (its replica died) is dropped. The stream's
  `presence_snapshot`, the agents endpoint and `@here` read the room's
  view. The owner-only announcement (`publishOwned`) is gone. Slow mode is
  one claim per member and channel that every replica shares
  (`realtime.Claims`, Redis `SET NX PX` on the bus's Redis), so a post
  reaching another replica is still held; without a bus, memory and the
  store decide as before.
  Left for slice 5 with the ownership: a member's desktop plugin stream
  and its deliveries (member-keyed routing), scheduled posts at the owner,
  plugin events and catalog hints sent to peers.
- **Slice 5 (2026-10-30): no owners.** Forwarding, release on view change,
  the hand-over watch (`handover.go`, `DeliverCommitted`), the peer set and
  `Leave` are gone from realtime; any replica serves any room and member.
  What crossed peers now crosses the bus's topics (`Replicas`,
  `services/realtime/replicas.go`): a hook-bearing room event to every
  replica's plugin streams; catalog hints; a member's notification to the
  replica holding their desktop stream (found by a mark, `realtime.Marks`,
  renewed every 10 s, gone 30 s after its replica) with the outcome back;
  a delivery's ack that reached another replica, to the one that sent it
  (named by the delivery id's prefix); the live replica count for per-replica
  budgets (a beat every 10 s). Scheduled and dispatched posts run where they
  land. A replica keeping a room's state receives the room from the bus
  meanwhile (`Hub.Watch`), so the other replicas' changes reach it. The bus
  is generic topics (`realtime.Topics`; `RoomBus` over it for room events).
  The chart runs realtime as a Deployment (an HPA may scale it), fails
  without a bus or S3 when scaled, and recreates a single pod on its disk
  volume. relay keeps its ownership.
  The failover drill passes: 6,000 of 6,000 sends, 19,900 of 19,900
  deliveries, 240 reconnects and 4 gaps (owned rooms: 564 to 884 and about
  450).
- **Slice 8 (2026-10-30): a database failover.** `store.Unavailable`
  classifies a broken or refused connection and MySQL's read-only (a writer
  failing over), full and shutting-down errors; a chat send that meets one
  is answered 503 with `Retry-After`, and the web client (and the load
  tool) send a 503 again for about 30 s more (5, 8, 8, 8 s after the usual
  0.4, 1.2, 3 s). RDS Proxy is in the deployment guide.
- **Slice 6 (2026-10-30): drills.** `scripts/failover-check.sh` takes a
  drill: `replicas` (drain, rejoin, crash, rejoin), `rolling` (each replica
  in turn), `scale` (3 → 5 → 3), `redis` (the bus's Redis restarted). All
  pass under the same load (300 members, 30 rooms, 50 msgs/s): every send
  stored, every delivery arrived, every stream open at the end. A scale
  event moves no stream (0 reconnects); a Redis restart reconnects none and
  tells every stream to read what it missed (`docs/load-testing.md`).
- **One Redis for every service's replicas (2026-10-30).** The owner, on
  hearing the gateway needed a peer list and relay its ownership:
  "能不能设计不要这么复杂？简单高效高性能高可用". Now the only setting a
  service needs to run more than one replica is `BNBLOC_REDIS_URL`
  (`BNBLOC_REDIS_CLUSTER` for a Redis Cluster), read by gateway, realtime and
  relay (`internal/redisbus`, moved out of realtime). `internal/shard` (peer
  discovery, probes, rendezvous hashing, forwarding, 1,200 lines with its
  tests) is gone, and so are the headless peer Services, StatefulSets and
  `_PEERS`/`_SELF` settings.
  - gateway: the replicas count each other (`redisbus.Census`, a sorted set
    beaten every 5 s) and every per-replica rate limit divides by the count.
  - relay: a session's first stream records `<pod IP>:<port> <token>` at the
    rendezvous (`redisbus.Meet`, SET NX); the second, landing on another
    replica, dials that one's `/internal/relay/join` with the token and pipes
    its stream to the parked one; a record of a dead replica is cleared and
    met again.
  - realtime: the same Redis; its replica count is the census too.
  - A setting of the former designs refuses startup with the fix
    (`redisbus.FromEnv`).
- **Slice 7, first part (2026-10-31): unread is pushed, not polled.** The
  web shell polled every room's unread counts every 25 s (about 8,000
  queries a second for 1,024 clients in 20 rooms of 5 channels). It now
  keeps one activity stream per tab (`GET /v1/my/activity?rooms=`,
  `member_activity.go`): the rooms' unread state once per connection, then
  per second what changed (`unread_delta`: new messages that count by the
  same rule, deletions, a mark of read on any device). The stream is one
  hub subscriber across the member's rooms and their own key
  (`Hub.SubscribeRooms`, `wire.MemberActivityKey`, where a mark of read
  goes), so it works on any replica over the bus. The shell counts each
  sequence once (`web/src/lib/unreadActivity.ts`), reads a room again when
  it was marked read, and every room every five minutes as a safety net.
  Cost per message: one in-memory access check per online member of the
  room; per mark of read: one event to the member's own streams.
  Superseded the same day by part B (§9): the activity stream became the
  member stream's unread.
- **The revocation window, closed (2026-10-31).** A member removed through
  one replica and a message stored through another just after were not
  ordered with each other: for the bus's milliseconds the removed member's
  stream on a third replica could get the message. A revocation
  (`member_removed`, `room_deleted`) now takes the room's next sequence
  (`ChatStore.NextRoomSeq`, one statement on the counter; it stores no row)
  and ends its batch, so a message stored after it has a higher sequence and
  every replica's reorder hold delivers the revocation first. A replica's
  live order starts at the counter (`RoomSeqHead`), not the highest row, so
  a revocation last in a room holds nothing. Left: a revocation whose
  publish was lost (its replica died between numbering and publishing) is
  waited for 300 ms like any missing sequence, then the room's streams read
  the difference and the removed member's stream ends at its next
  membership check (every 15 s). Section access changes
  (`section_membership`) are numbered too since part B.
- **Part B built (2026-10-31): one stream per client (§9).**
  `GET /v1/my/events` with `POST /v1/my/events/scope`
  (`services/realtime/member_stream.go`); the open room's rules are the
  room stream's own (`roomView`, `room_view.go`, one owner for both);
  `/v1/my/activity` and the room stream's scope route are gone. A scope
  reaching a replica that does not hold the stream is handed to the one
  that does over the bus (the stream id names it). Web: one `MemberStream`
  per tab in `sse.ts` (`watchRoom`, `watchUnread`); the room view, thread
  and people-chat panels and the shell share it.
  - **Found by the load run, not by the tests: a bus handler waited on the
    bus.** A forwarded scope was answered inside the bus's receive loop;
    applying it subscribes rooms, whose confirmation that same blocked
    loop delivers. Each forwarded scope stalled the replica's whole bus
    for the 5 s timeout: 119 of 300 streams failed to scope, 149 gaps,
    delivery p99 1.6 s. Calls are answered on goroutines of their own
    now, the `Topics` contract says deliver must not wait on the endpoint,
    and the in-memory bus delivers from one receive loop per endpoint as
    Redis does, so the replica tests deadlock as production did (checked:
    the old handler fails `TestMemberStreamUnreadAcrossReplicas`).
  - Results on the 3-replica HA stack (laptop load average 40 to 70):
    300 members in 30 rooms of 3 channels at 50 msgs/s: 300 of 300 streams
    open at start, connect p95 10 ms, 5,100 of 5,100 deliveries, p99
    28 ms, 0 gaps, told of 15,000 of 15,000 new messages. 1,024 members in
    one room of 16 channels at 100 msgs/s: 192,000 of 192,000, 0 gaps,
    0 disconnects, 3,072,000 of 3,072,000 told. Failover drill (drain one
    replica, crash another, under load): 19,885 of 19,885, 0 lost,
    0 gaps, PASS.
  - Needs a desktop release: the desktop app's bundled web client speaks
    the old routes until then.
- **A stream's unread state costs about one query (2026-10-31).** The
  member stream sends its rooms' unread state on every connection, so a
  deploy that reconnects everyone asks it of every member at once. It was
  two queries per channel of every room (`UnreadCount` for each), and one
  membership probe per room. Now:
  - one query per tenant and 200 rooms finds the channels with anything
    unread (`ChatStore.UnreadChannels`, the candidate test of
    `UnreadRooms`, one SQL owner for both), and only those are counted;
  - one membership probe per 200 rooms (`GET /v1/users/{id}/memberships`
    on bnbloc-room, answered only to the member themselves;
    `membership.LookupMany`), with fresh cached answers not asked again;
  - the membership cache sweeps expired answers once a TTL instead of
    scanning the whole cache on every answer it keeps (a reconnect burst
    was quadratic under the cache's lock).
  Measured on the HA stack, 800 streams of members in 4 rooms of 8
  channels: 66,995 statements to open them before, 12,500 after (the
  unread part 51,000 to 950, about one per stream); connect p95 75 to 7 ms.
- **The stream reads its rooms itself (2026-10-31).** The client named the
  rooms whose unread it carried, at most 500: a member in more rooms had
  no live unread for the rest, every name was checked on every scope, and
  a room someone added the member to stayed off the sidebar until the next
  navigation. The stream now reads the member's rooms from the room
  directory and follows joins and departures (§9 "Rooms are the
  server's"). Every writer of a membership announces it: the creator's
  join is announced too, and org sync announces its joins through realtime
  (it wrote memberships without `member_joined`, so its members also
  missed the room's standing content shares);
  `TestMembershipWritersAnnounce` fails on a writer that does not.

## 9. Part B, the end state: one stream per client

The owner (2026-10-31): "我们开发要保证一步到位，一劳永逸". A web tab held up
to four streams: the room view's lanes stream, a thread panel's and the
people-chat panel's one-channel streams, and the activity stream. The end
state is one.

- **`GET /v1/my/events`** (SSE, the web app's only stream): it opens with
  `stream_opened {streamId}` and the unread state; the client then scopes
  it with **`POST /v1/my/events/scope`** `{streamId, open?, state?}` and
  again whenever the room it shows changes, never reopening it.
  - **Rooms are the server's.** The stream carries the member's rooms in
    its tenant, read from the room directory when it opens and after a gap
    (`MemberRoomIDs`, then one membership probe a page), and follows them:
    a join reaches it on the member's own key (`member_rooms`, which
    realtime announces when bnbloc-room publishes `member_joined`, the
    creator's join and org sync's included), a removal or a deletion on the
    room itself. It tells its client (`member_rooms {joined, left}`), whose
    sidebar reads its rooms again, and sends the unread state again. The
    client names no rooms: there is no list to check, no bound on it, and a
    room someone else adds the member to appears at once.
  - Unread: `unread_state` when the stream opens, when its rooms change,
    after a gap and on `state: true` (a new watcher), `unread_delta` each
    second after.
  - `open`: `{roomId, channels, since}`, the room the tab shows and the
    channels open in it (the room view's, a thread's, the people chat's;
    at most 4). For it the stream is what a room's lanes stream is: the
    replay after `since` (or `stream_gap` when the buffer does not reach
    back), the presence snapshot, full events of the open channels and
    room-wide ones, `channel_activity` for the others, the access rule of
    members-only sections, a preview's narrower kinds, presence counted
    while it is open. Switching rooms leaves the old one (presence) and
    opens the new one on the same connection. A room the member is not in
    (a preview) is received while open.
  - Access to the open room ending (removed, deleted, or found gone at the
    15 s re-check) is told as the revocation event; the stream goes on.
- The member stream is one hub subscriber on every room it carries
  (`Hub.SubscribeRooms`, rooms added and removed as they change; a replica
  joins a stream's rooms on the bus 16 at a time) and the member's own
  key. It runs on any replica over the bus.
- **The room stream** (`/v1/rooms/{id}/events`) stays for what is not a
  person's web tab: bridges and agent hosts (`presence=0`, `agents=`,
  `replay=0`), and the standalone preview and live pages. The activity
  stream (`/v1/my/activity`) is gone.
- **Web**: one member stream per tab (`sse.ts`); the room view, thread
  panel and people-chat panel watch their channels on it
  (`useRoomStream`), the shell its unread and its rooms. The load tool's
  clients hold one member stream each.
- **Section access is ordered too**: `section_membership` takes the room's
  sequence as the revocation kinds do, so a message of a members-only
  channel stored after a member left its section never reaches their
  stream on another replica.

Placement: platform (realtime's stream contract and the web client's).
