# Live channel: a low-latency session for a file or an app, for editing together and for playing together (2026-10-26)

Status: proposed (2026-10-26). Nothing is built. The baseline below was
measured on the QA gateway the same day. Builds on
`docs/prd/2026-10-10-enterprise-scale.md` (rooms are owned by one realtime
replica; `cmd/bnbloc-loadtest`) and `docs/prd/2026-10-26-excalidraw-previewer.md`
(the first user).

## Why

Two asks, one mechanism.

1. **Edit one drawing together.** The Excalidraw previewer is single-editor. Two
   members who open the same file edit apart: the version store refuses the
   second save (compare-and-swap), nothing is merged, and that member must
   reload and redo the work.
2. **Play together.** The product owner has built small games on the platform as
   interactive apps in a room. They have no fast shared state today: see "What
   exists". (Which of them need it, and at what rate, is not surveyed here.)

The product owner's condition: **performance is the point.** A collaborative
editor whose cursor lags, or a game that stutters, is not worth shipping. So
this record starts from a measurement, sets budgets, and orders the work so
that every optimization lands with a before/after number.

## What exists (checked against the code, 2026-10-26)

One live path serves previewers (`liveState`) and apps (the runner's
`artifact-state`). One message takes this route:

| # | Hop | Where | Cost on every message |
|---|---|---|---|
| 1 | frame → host, `postMessage` | `web/previewer-sdk/src/protocol.ts` (`preview.state.send`, ≤ 16 KiB) | structured clone of a JSON value |
| 2 | host → gateway, `POST /v1/rooms/{room}/artifacts/{id}/state` | `web/src/lib/api.ts` `artifactState` | a full HTTP request, session auth, membership (cached 30 s: `services/gateway/cache.go`) |
| 3 | gateway → room service | `services/room/artifacts.go` `handleArtifactState` | `IsMember` and `GetArtifact` store lookups; JSON decode of the body; `json.Unmarshal` of the state into `any` |
| 4 | room → realtime, `HTTPPublisher.Publish` | `internal/realtime/publisher.go` | `json.Marshal` of the event; a signed HTTP POST, 5 s timeout, **awaited before the response** |
| 5 | realtime owner replica | `services/realtime/scaleout.go` (`shard.Forward`) | a further hop when the request lands on a replica that does not own the room |
| 6 | `Hub.PublishCommit` → `deliver` | `internal/realtime/hub.go` | one **Hub-wide mutex** (`h.mu`) held while every subscriber of the room is written; a subscriber whose buffer is full has its **whole room stream closed** (`dropLocked`) and reconnects through history |
| 7 | SSE frame → client → host → frame | `web/src/lib/sse.ts`, `RemotePreviewer.tsx` | a JSON parse per event, then `postMessage` |

What the path gives, and does not give:

- **No ordering.** Each message is its own request; two in flight can arrive
  in either order.
- **No backlog.** `artifact_state` is a transient kind (`wire.RoomEventKinds`):
  not durable, not replayed. A late joiner reads one value: the last state,
  held in memory by the room service (`liveStates`, at most 1,024 artifacts,
  per process).
- **16 KiB per message** (`wire.MaxArtifactStateBytes`). A real drawing does not
  fit: the architecture drawing in the room that prompted this is 43 KB for 50
  elements; the repo's sample is 5.9 KB for 7.
- **Room-wide fan-out.** Everyone subscribed to the room's stream receives
  every state message, whether or not they have the file open.
- **A slow receiver costs everyone**: the single global lock, and a closed
  chat stream for the slow one.

Existing precedent this design reuses: WebSocket is already carried through
Caddy and the nginx ingress with long timeouts (the desktop bridge's `/relay`,
`deploy/Caddyfile`, `deploy/k8s`), `github.com/coder/websocket` is already a
dependency (`internal/wsconn`), and `shard.Forward` already carries a
WebSocket upgrade to a key's owner.

## Measured baseline

Throwaway room on QA, one sender and one receiver on one SSE stream, from a
laptop over the internet, open-loop sends (a send is due by the clock, not when
the last one answered). The tool was a temporary Go test using the CLI's
stored sign-in; the room was deleted afterwards. Network floor, a bare
`GET /healthz`: **p50 126 ms, max 529 ms**.

| Rate | Payload | Messages | POST p50 / p95 / max | End to end p50 / p95 / p99 / max | Lost | Reordered |
|---|---|---|---|---|---|---|
| 1/s | 100 B | 4 | 126 / 132 / 142 ms | 126 / 132 / 132 / 143 ms | 0 | 0 |
| 10/s | 100 B | 50 | 143 / 260 / 401 ms | 143 / 251 / 354 / 377 ms | 0 | 0 |
| 10/s | 4 KiB | 50 | 185 / 517 / 629 ms | 185 / 533 / 604 / 629 ms | 0 | 0 |
| 30/s | 100 B | 150 | 153 / 343 / 495 ms | 153 / 327 / 463 / 495 ms | 0 | **5** |
| 30/s | 4 KiB | 150 | 407 / 806 / 901 ms | 407 / 806 / 858 / 901 ms | 0 | **11** |

What this shows, and what it does not:

- Idle, the server adds almost nothing: at 1 message a second the end-to-end
  time equals the network floor.
- Latency grows with rate and size: at 30/s of 4 KiB the median is 407 ms, about
  280 ms above the floor, and the p99 is 858 ms.
- **Messages arrive out of order** at 30/s (3% and 7% of messages). A cursor or
  a game input stream cannot be built on this without its own sequence numbers.
- No loss and no throttling (no 429) in this range.
- **It does not say where the time goes.** One client over a WAN, no fan-out,
  no server-side timing. The first piece of work is therefore the measurement
  (M0), not an optimization.
- It is one sample on one day. Treat the numbers as the shape of the problem.

## Goals and budgets

Targets, **not yet measured**; M0 confirms or revises them, and a target that is
never measured is dropped. What the platform controls is the time *it adds*:
the end-to-end time for a distant user is at least the network round trip, and
no server work removes that.

1. **Server-added latency**, from a frame arriving at the owner replica to the
   last receiver's frame leaving it: p50 ≤ 1 ms, p99 ≤ 5 ms at reference load R1.
   p99 ≤ 20 ms at R2.
2. **Reference loads.** R1: one session, 8 participants, each sending 30
   messages a second of 200 B (240 in, 1,680 out a second). R2: one session,
   32 participants, same rate (960 in, ~30,000 out a second). R3: 200 sessions
   of 8 at 10 a second at once (cross-session interference: a busy room must not
   slow a quiet one). R4 (an arena): one server participant and 64 players;
   each player sends an input of 30 B at 30 a second, the server sends each
   player its own 500 B view at 20 a second (1,920 in, 1,280 out a second, every
   outbound frame different, so "encode once" does not apply).
3. **Isolation.** A receiver that stops reading costs the others nothing and
   does not close its chat stream.
4. **Memory.** ≤ 64 KiB per connection at rest; a session's ring is bounded.
5. **Allocations.** The relay's hot path allocates at most once per message.
6. **Join.** From a join to the first snapshot: one round trip plus ≤ 20 ms of
   server time.
7. **Order.** Within a session, every receiver sees `event`s in one order.
8. CPU per 1,000 outbound messages a second is reported at M0; the cap is set
   from the report.

## Design

### The live session

A **live session** is the short-lived meeting point of everyone who has the
same file or app open. Its key is (tenant, room, artifact). The platform names
no content: the payload is opaque bytes, and the server never parses it.

### Transport

- **One WebSocket per client tab**, `GET /v1/rooms/{roomId}/live`, routed to
  the room's owner by `shard.Forward` (as room-scoped requests already are), so
  the live path has no inter-service hop.
- **Authorize once, at join**: room membership, the artifact belongs to the
  room, the room's read gate. A member may send; a reader (a visitor of a shared
  room) may only receive. After that the connection is a bound identity: no
  per-message HTTP, auth or store lookup.
- **One socket carries several sessions** (a tab with three files open uses one
  connection). The *host* owns the socket, not the frame: previewer frames are
  sandboxed with `connect-src 'none'`, and apps run in a sandboxed runner.
  Messages cross the frame boundary by `postMessage` with a transferred
  `ArrayBuffer` (no copy).
- The frame is **binary** and the sender is stamped by the server, never
  claimed by the client. Sketch, to be fixed in M1: `type | class | session |
  seq | payload`.
- **Where it terminates**: in the realtime service (it owns the room's state),
  behind the gateway's reverse proxy at first (Go's `ReverseProxy` carries an
  upgrade). Going straight to realtime, as `/relay` goes straight to the relay,
  saves a proxy hop and costs a second place that verifies sessions; it is
  decided by M0's numbers, not assumed.

Considered and rejected for the base:

- *SSE and POST, as today*: a request per message.
- *WebRTC data channels, peer to peer*: the lowest latency and no server work,
  but it needs signalling and TURN (egress here is allowlisted), and there is no
  central authorization, revocation or audit. May return as an optimization.
- *WebTransport or QUIC datagrams*: no head-of-line blocking, but support in
  the desktop app's WKWebView and in the ingress is unverified. The frame format
  is transport-neutral so this can be revisited.

### Message classes (the base contract)

| Class | Meaning | Server behaviour |
|---|---|---|
| `state` | latest wins, keyed (a cursor, a player's input, a selection) | keeps only the newest per (sender, key); a slow receiver gets the newest, never the backlog |
| `event` | ordered, small (an edit delta, a move) | the server assigns the session's sequence (one owner, so one order, no clocks); keeps a ring of the last N events or M KiB, whichever first; receivers detect a gap by sequence |
| `snapshot` | one slot per session, large | a participant posts `(baseSeq, bytes ≤ 1 MiB)`; the newest `baseSeq` wins; a joiner receives the snapshot, then the events after its `baseSeq`; memory only, dropped after the session has been empty for a grace period |
| `presence` | who is here | derived from the sockets by the server, not self-reported |

Sizes: `event` and `state` payloads ≤ 16 KiB (the limit today); a `state` key
≤ 64 B; a snapshot ≤ 1 MiB (about a thousand drawing elements).

Three more parts of the contract, needed once a session has many players or a
server-side authority (see "Third user"):

- **Addressing.** A frame may name its receiver: one participant, or all. Without
  it a game cannot send each player only what that player may see (an arena's
  nearby players, a card hand).
- **Server time.** Every frame carries the server's monotonic time, and a join
  starts a round-trip probe that the SDK exposes. A client interpolates remote
  entities on that clock; it needs no clock of its own to agree with anyone's.
- **Named sessions.** The key is (tenant, room, artifact, name), so one game can
  run many arenas (`arena-7`) and one drawing a single default one. Assigning a
  joiner to an arena is the game's logic, not the platform's.

### Fan-out and back-pressure

- **Encode once.** One immutable frame is written to every socket; no
  per-subscriber copy and no JSON.
- **No global lock.** Each session has its own owner goroutine (or its own
  mutex). The live path does not touch `Hub.mu`.
- **Bounded queue per socket.** For `state`, a newer message replaces an older
  one for the same key in place (coalescing). For `event`, a socket whose queue
  passes its bound has its queue dropped and receives one `resync` frame: it
  reloads from the snapshot. The room never waits, and the chat stream is a
  different connection and is never closed because a live socket was slow.
- **Admission.** A token bucket per connection (messages and bytes a second;
  excess `state` is coalesced, excess `event` is refused with an error frame),
  a cap on sessions per room, participants per session, and sockets per member,
  and a per-tenant cap. All are settings with defaults, not constants.
- **Revocation.** `member_removed` and `room_deleted` already exist as hub
  events; they close the affected live sockets.

### Host, SDK and previewer contract

- One owner on the web, `web/src/lib/liveChannel.ts`: the socket, reconnect
  with backoff and jitter, resume from the last sequence, multiplexing.
  Previewers and apps both go through it.
- Previewer capability `live` (replacing `liveState`) with `preview.live.*`
  messages: `join`, `send {class, key?, payload}`, `recv`, `presence`, `resync`,
  `snapshot`. The SDK exposes them to a frame.
- App SDK (generated by the platform into every room's records): `app.live`
  with the same four classes, for games and boards.
- **Clean cutover** (pre-launch, no compatibility): remove `POST …/state`,
  `EventArtifactState`, the room service's `liveStates` cache, `liveState` and
  `preview.state*`, and the runner's `artifact-state`. Callers to migrate: the
  sprite-sheet previewer, `RemotePreviewer`, `ArtifactRunner`, `artifactFrame`,
  `StandalonePreview`, the room view's event handling, and the SDK demo;
  `services/bridge/bridge.go` also names it and is to be checked. Apps built
  against the old message must be regenerated; QA's list of such apps is read
  before the cutover.

## First user: the Excalidraw previewer (a component)

- **Deltas as `event`.** Only elements whose `version` changed since the last
  send, batched once per animation frame and coalesced. Excalidraw's own merge
  is used: `reconcileElements` keeps, per element, the higher `version`, and on a
  tie the **lower** `versionNonce`, so repeating or reordering an update changes
  nothing. One exception, read from its source (`data/reconcile.ts`): a remote
  update to an element the local member is editing at that moment (text being
  typed, a resize, a new shape) is dropped. Convergence therefore also needs a
  periodic full sync, which Excalidraw's own collaboration does too (every 20 s,
  `SYNC_FULL_SCENE_INTERVAL_MS`); here that is the snapshot, not an extra
  mechanism. It is last-writer-wins per element: two people changing the same
  element at once keep one change, not both
- **Cursors and selections as `state`**, at most 30 a second, coalesced.
- **Remote edits do not enter the local undo stack** (`captureUpdate: NEVER`):
  Ctrl+Z undoes only one's own steps.
- **Join** receives a snapshot and the events after it, and applies them with
  `reconcileElements`. A `resync` reloads from the snapshot. After a replica
  move (the session's memory is lost), whoever has the newest state posts a
  snapshot again.
- **Snapshot policy** (the component's): after a short quiet period or every N
  events, by one participant at a time. Snapshots are compressed; deltas are not
  (compression costs more than it saves at that size; measured, not assumed).
- **Durable save is separate and unchanged in kind:** the version API with
  compare-and-swap. Autosave: debounced, one saver, and on a conflict the loser
  merges the newer version in with `reconcileElements` and retries. At most one
  autosave a minute, so history is not flooded.
- **Agents.** An agent's edit arrives as a new version. While a session is live
  it is merged into the live scene instead of reloading the frame, which today
  could discard a member's unsaved edits (not verified: depends on whether the
  workbench rebuilds the frame per version).
- Only the latest version takes part; an older version is read-only.

### Later slice: an observer authority (not in the first version)

The first version is peers and the relay, the shape Excalidraw's own
collaboration has (a relay that forwards; a newcomer waits up to 5 s for a peer's
scene; clients persist; a full sync every 20 s). A server-side participant, as
the .io section describes, can be added to it later **as a listener, not in the
path**: peers still send through the relay directly, so live latency does not
change.

- **What it does.** A service component (it declares `live`) joins each drawing
  session, applies every `event` to its own copy of the scene with the same merge
  rule, posts the `snapshot` itself, and does the autosave to a version.
- **What it removes from the first version.** The client snapshot policy, the
  election of one saver, the clients' autosave conflict retries, and "whoever has
  the newest state re-posts it after a replica move". A joiner gets the whole
  document from it even when every editor has left. It refuses a malformed delta
  or an oversized snapshot into its copy and posts the good snapshot, so a bad
  participant cannot poison a joiner. It can also apply an agent's edit as events
  (open: through the version API, which it watches, or through a service
  primitive), and it keeps a log of who changed which element.
- **What it does not change.** Live latency (it is not in the path) and merge
  quality: two people changing one element at once still keep one change.
  Finer-grained merging (text inside a shape) is a different piece of work.
- **What it costs.** The merge rule is JavaScript (`reconcileElements`). Go would
  have to reimplement it (a risk of two behaviours, held in check by the shared
  convergence test vectors), or the service runs the same module in a Node
  process (a runtime this deployment has not been checked for). It is a stateful
  service: one authority per session, sharded with the room owner, and on failure
  it rejoins and rebuilds from the last version and the snapshot.
- **When to build it.** Not on a schedule. M3 counts resyncs, snapshot posts by
  clients, autosave conflicts, and joins that missed unsaved edits; it is built
  when those numbers, or the wish for an agent to edit live, say the first
  version's workarounds hurt. Placement: a **component**, because it has its own
  runtime. The snapshot and autosave policy then move out of the previewer in the
  same change; they are not kept beside it.

## Second user: games (content, on the same API)

- **Turn-based and lockstep games** use `event`: the server's single order
  gives every player the same sequence, so applying it deterministically keeps
  everyone in step.
- **Action games** use a **host-authoritative peer**: the first participant in
  join order runs the rules, sends the world as `state` at 20 to 30 a second
  (coalesced), and receives the others' inputs as `state`. The host leaving is
  seen in `presence`; the next in join order takes over from the last state.
- **What this is not.** The channel is a relay, not a game server. There is no
  server-side rule engine, no anti-cheat and no hidden information (every
  participant receives what is sent). It suits casual games among colleagues in
  a private room. A competitive game, or one with secrets, needs server
  authority: that would be a **service component** (it passes the Replace test),
  not this base.
- **The floor is the network.** Players in the same region can play at 20 to 30
  updates a second; the platform removes its own share of the delay, not the
  distance.
- A sample game lives in a content repository, never in the platform.

## Third user: .io-style games (a service component on the same base)

An .io game (an arena of dozens of players, a server that decides what happens,
each player seeing only what is near them, prediction and interpolation on the
client) cannot run on a browser host, and the first version of this record said
so only in one line. The reasons, and what the base then needs:

- **A browser host is the wrong authority.** Every input goes player → server →
  host → server → players, so the host's round trip is added to everyone's. A
  browser also throttles a tab that is not in front, which would stall the whole
  arena. The base therefore allows a **server-side participant**: a service
  component that joins a session with its own signed credential
  (`component.Sign`, as service components already call the platform), receives
  every input, runs the game loop, and sends frames. It is a new service
  primitive, `live`, that a package declares like the others
  (`pkgmeta.validateServicePackage`; `servicecomp.Declares` at the route).
- **Per-player views need addressing** (above). That also removes the old "no
  hidden information" limit: a service authority can deal a hand to one player
  and a different view to another.
- **Rate and size are a bandwidth budget, not a latency budget.** Arithmetic,
  not a measurement: 100 players at 20 frames a second of 500 B is 1 MB/s out, 8
  Mbit/s; at 30 a second of 1.5 KB it is 4.5 MB/s, 36 Mbit/s, for **one** arena.
  Inputs in are small (about 90 KB/s). So the realistic size is tens of players
  an arena, with delta frames and an area of interest to reach a hundred. The
  per-session participant cap becomes a setting (default 32, raised per
  deployment), and R4 is the load that checks it.
- **Frames are no longer shared.** With a view per player, nothing is encoded
  once; the cost moves to the authority's serialization and to one write per
  socket. The plan's item 8 matters more here: one write per socket per tick, and
  a bounded queue that coalesces `state` (a player who falls behind gets the
  newest view, never the backlog).
- **Prediction and interpolation are the game's code**, in an SDK helper in
  content or the SDK: server time and the probe give it the clock; the platform
  decides nothing about movement.

Placement: the game loop, the rules and any anti-cheat check are a **service
component** (its own runtime and settings; an organization swaps it). The
platform ships the session, addressing, server time and the `live` primitive.
Durable things (a score table) use the apps' existing data store, not the
channel. Which service is a game's authority is declared by the app that is
published (the person who publishes it consents), and the platform joins it only
to sessions of that artifact, in the room where the artifact lives.

What stays out of scope: a public game open to anonymous people. Links default to
the organization's sign-in; the model is colleagues in a room, dozens to a
hundred at once, not thousands at once, and the egress cost above is the reason
to keep it so.

Added review notes for this section:

- *Abuse.* A service participant is trusted with every input of its sessions, as
  the service primitives already are, and is limited to the sessions its app
  named. A player's frames are stamped by the server, so an input cannot claim to
  be another player's. Per-connection token buckets apply to players as before.
- *Contract.* `live` joins the service primitives; the package validator and
  `servicecomp.Declares` gain it; a service that does not declare it gets none.
- *Failure.* The authority's process may die or restart: presence shows it left,
  players see the game pause, and the service rejoins and rebuilds from its own
  store; the channel holds no game state beyond the snapshot slot.
- *Pre-mortem.* "The arena stutters at 60 players": the first suspect is the
  authority's per-receiver serialization and the per-socket write, so R4 reports
  both, and the cap is set from that number, not from the table above.

## Optimization plan

Ranked by expected effect. Each lands with a before/after row in "Results".

| # | Change | Removes | Verified by |
|---|---|---|---|
| 1 | Persistent socket; authorize once | per message: an HTTP request, session auth, two store lookups, two JSON decodes and encodes, the room→realtime HTTP hop (three HTTP hops and two lookups become none) | in-cluster p50/p99 at R1 |
| 2 | Opaque relay, encode once | JSON re-encoding per message and per subscriber | allocations per message (`go test -bench -benchmem`), pprof |
| 3 | Per-session owner, no `Hub.mu` | cross-room contention | R3: a quiet room's p99 while a busy one runs |
| 4 | Coalescing queues and `resync` | a slow receiver closing streams and holding memory | harness client throttled to 10% of the rate |
| 5 | Per-frame batching and delta encoding (component) | message count and bytes | messages and bytes per edit; frame time at 8 editors |
| 6 | One socket per tab; transferred buffers across the frame | per-frame connections and copies | host CPU at R1 |
| 7 | Owner routing for the live path | an inter-service hop | hop count in a trace |
| 8 | Server tuning: one write per flush per socket, no per-message compression for small frames, pooled buffers, `GOMEMLIMIT` review | syscalls, GC pauses | GC pause histogram at R2 |
| 9 | Only if the numbers ask for it: a direct route to realtime; WebTransport datagrams for `state` | a proxy hop; head-of-line blocking | M0-style comparison |

Method:

- **M0 is the harness.** Extend `cmd/bnbloc-loadtest` with a live mode, run it
  **inside the cluster** (to take the WAN out) and from a laptop (to see what
  users see), and add server-side metrics: ingress-to-egress latency histogram,
  queue depth, coalesced and resync counts, active sessions and sockets.
- A change that does not move a number is reverted, not kept.
- Convergence is tested separately from speed: a property test applies one set
  of events to several replicas in random order, with duplicates and drops, and
  they must end identical.

## Placement

| Part | Where | Why | What an organization would swap |
|---|---|---|---|
| Sockets, join authorization, fan-out, ring, snapshot slot, limits, audit, kill switch | **Platform** | identity and the attested caller; isolation between rooms and tenants; events only the platform sees; the budget and the off switch. Performance also depends on running in the realtime process | its limits and its switch |
| The frame format, the `live` capability and `app.live` | **Platform** (contract) | the contract between host and frame, with a conformance test | nothing |
| Drawing deltas, merge, cursors, snapshot policy, autosave policy | **Component** (Excalidraw previewer) | its own viewer; a second editor could be installed beside it | the whole previewer |
| Game rules, lobby, host election, rendering, sample games | **Content** (interactive apps) | guidance and examples people copy and change | the games |
| Server-authoritative games, anti-cheat | **Component** (a service), later | needs its own runtime and rules | the service |

Tests. *Name*: the platform names no drawing, game, cursor or element; classes
and sessions only. *Fork*: an organization that wants its own editor installs
one; the platform is unchanged. *Replace*: a second live-capable previewer, or
a service component, installs beside this one. *Reason*: platform parts are
identity, isolation, events, budget and the contract.

## Design review

1. **Actors.** Members of the room (send and receive), readers of a shared room
   (receive only), agents (they edit through versions; no new tool or rule for
   them). Two members editing one element resolve per element by version.
2. **Time and lifecycle.** A session exists while sockets are in it, plus a short
   grace period for the snapshot. A replica move or restart closes the sockets
   (`ReleaseRooms`); clients reconnect to the new owner, the in-memory session is
   gone, and the first participant to hold newer state re-posts a snapshot. The
   durable truth is always the last saved version, so the worst case is losing
   the edits since the last autosave, and the client says so.
3. **Scale and cost.** Per socket memory and CPU, no model cost. R1 to R3 define
   the load; the per-session, per-room, per-member and per-tenant caps bound it.
   Fan-out is per session, not per room: members who do not have the file open
   receive nothing.
4. **Failure and recovery.** A dropped socket reconnects with backoff and
   resumes from its last sequence. Past the ring, it gets `resync` and reloads
   from the snapshot. If the socket cannot be opened (a proxy blocks WebSocket)
   the file opens single-editor and says why; there is no second transport. The
   desktop bridge already depends on WebSocket through the same edge, which is
   the evidence that the deployment passes it.
5. **Boundaries and privacy.** Authorized by room membership at join; keyed by
   tenant; revoked on removal or deletion. Payloads are never stored and never
   logged; the snapshot is memory only. A shared room's visitors read only.
6. **Abuse and untrusted input.** The server stamps the sender. Token buckets,
   size caps and the caps above bound a flood. The payload is opaque to the
   server and **validated by the receiving component** (the Excalidraw reader
   refuses what is not a scene). A participant can post a bad snapshot to a
   joiner: inside a room that is the same trust as editing the file, bounded by
   the member role and by the durable version behind it.
7. **One rule, one owner.** One server package for sessions, one web module
   (`liveChannel.ts`) for the socket, one set of generated wire constants, one
   SDK. The old live path is deleted, not kept beside it.
8. **Defaults and fallbacks.** No socket: single-editor with a sentence. No
   snapshot: the saved version. Limits are settings with defaults.
9. **External contracts.** A new WebSocket endpoint, a frame format, the
   `live` capability, `app.live`; the removal of `artifact_state` and
   `liveState`. An older host ignores a capability it predates.
10. **Load on users and agents.** None for agents. Members see presence and
    cursors; nothing to configure.
11. **Release and compatibility.** Pre-launch cutover, in order: realtime and
    gateway, web, the previewer (`--rollout optin` first), content. QA first;
    QA apps that use the old message are regenerated.
12. **Observability.** The histogram, queue depth, coalesced, resync, joins,
    leaves, limit hits and sockets by session; no payload in any log.
13. **AI capability.** There is no model call on the hot path: a step with no
    decision in it is a record, not a prompt. The AI entry points are existing
    ones: Ask AI on a selection, and an agent's edit merged live. A visible
    "an agent is editing this file" marker can come from the existing job events;
    it needs no new mechanism.
14. **Content, component or platform.** See Placement.

**Pre-mortem.** It shipped, and a month later it failed badly. The likeliest
reasons:

1. *"It is no faster for the people who complained."* Users far from the cluster
   are bounded by distance, and a change that is faster on paper but not in the
   numbers is not a change. The gate is the harness (M0), the budgets are the
   server's share only, and users are told what that means.
2. *"Two people edited and the drawing diverged, or an edit vanished."* Merging
   is Excalidraw's own `reconcileElements`; a snapshot and `resync` close gaps;
   the durable version is the floor; the convergence property test runs in CI.
3. *"One slow laptop made a room stutter for everyone."* A bounded queue per
   socket, coalescing, `resync` instead of waiting, no shared lock; the harness
   runs a throttled client on purpose.
4. *"A corporate network blocks WebSocket."* Named above; the single-editor
   fallback is explicit, and the bridge already relies on the same edge.
5. *"A game cheated, or had a secret leak."* Stated up front: the base is a
   relay for trusted rooms; server authority is a separate component.

## Milestones

Slices of the end state above, in this order; none is a stopgap.

- **M0 Measure.** Harness (live mode), server metrics, in-cluster baseline of the
  current path and the budgets confirmed or revised. Gate: this record's
  "Results" table has the baseline.
- **M1 Server.** The live session in realtime: WebSocket, join, four classes,
  ring, snapshot, limits, revocation, metrics; benchmarks and the harness at
  R1 to R3.
- **M2 Web and SDK.** `liveChannel.ts`, the frame contract, `app.live`; the
  cutover of the old path and the sprite-sheet previewer.
- **M3 Excalidraw.** Deltas, merge, cursors, snapshot and autosave policy, the
  agent-edit merge, and the convergence test.
- **M4 Games.** A sample turn-based game and a sample host-authoritative game in
  a content repository, with their guidance. Then the `live` service primitive,
  addressing, server time and named sessions, with a sample arena game as a
  service component and R4 as its gate.
- **M5 Optimize from the numbers.** Items 8 and 9 of the plan, only as the
  harness asks.
- **M6 Observer authority for drawings (only if M3's counts ask for it).** The
  service component above, on the `live` primitive from M4; the previewer drops its
  own snapshot and autosave policy in the same change.

## Open decisions (for the product owner)

1. Behind the gateway or straight to realtime: decided by M0's numbers.
2. Defaults for the caps (participants per session, sockets per member, ring
   size) and the 1 MiB snapshot limit.
3. Remove `artifact_state` in M2 (clean cutover, QA apps regenerated), as the
   no-compatibility rule says.
4. Whether the first games are turn-based only, or host-authoritative action
   games from the start.

5. Whether .io-style games are wanted at all in the first year. They need the
   service primitive and carry a bandwidth cost per arena (see the section
   above); the base is designed so that adding them later is an addition, not a
   rewrite, and that is the only part decided now.

6. For the observer authority, whether the merge is reimplemented in Go or run as
   the same JavaScript module in a Node service: decided when it is triggered,
   from whether this deployment can run Node as a service component.

## Results

(Empty until M0. Each optimization adds a before/after row here.)
