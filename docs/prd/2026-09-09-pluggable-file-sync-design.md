# Design — Pluggable Real-Time File Sync for bnbloc Rooms

Status: **implemented** in `services/bridge/sync` (engine + relay transport) and
`services/bridge` (per-room wiring). Supersedes the mutagen-only approach after
mutagen proved unrunnable in this environment (unsigned binary SIGKILLed by
macOS) and architecturally awkward (needs an SSH transport wrapped over the
relay). Two requirements drive this design:

1. **Pluggable** — the sync mechanism is an interface; the Go engine is the
   first implementation and can be replaced (e.g. by mutagen later) without
   touching the bridge.
2. **Higher-order conflict resolution** — better than last-writer-wins: detect
   true concurrent edits and merge them, never silently lose data.

## Where this runs

Inside the bridge (one per member, beside that member's local agent runtime). The bridge
already: dials outbound to the gateway, drives the local core, and knows the
room. It gains a sync engine that mirrors a room's project directory to peers.

Transport is the existing **bnbloc-relay raw stream**: `GET /relay/stream/{sessionKey}`
hijacks the connection and pipes raw bytes bidirectionally between the two peers
sharing a sessionKey. It is a reliable, ordered TCP byte stream — a perfect
carrier for a custom sync protocol, and it needs no SSH, no second listener, and
traverses NAT because both peers dial out.

## Pluggability: the SyncEngine interface

```go
// SyncEngine mirrors a local directory with one or more peers over a transport.
type SyncEngine interface {
    // Start begins syncing dir with the peer reachable over conn (a relay raw
    // stream). It runs until ctx is cancelled or Stop is called.
    Start(ctx context.Context, cfg SyncConfig) error
    Stop() error
    Status() SyncStatus
    Name() string // "gosync", future "mutagen", ...
}

type SyncConfig struct {
    Dir        string          // project directory to mirror
    Conn       net.Conn        // relay raw stream to the peer
    PeerID     string          // remote peer subject (for vector clock + logs)
    SelfID     string          // this peer subject
    Ignore     []string        // ignore globs (.git, node_modules, ...)
    OnConflict func(ConflictEvent) // surfaced to the room, never swallowed
}
```

- The bridge selects an engine by name from a small registry. Default `gosync`.
- A future engine (mutagen wrapper, etc.) implements the same interface; the
  bridge is unchanged. Selection is per room via a `syncEngine` hint (defaults
  to the room's `syncMode`: `mutagen` mode -> whichever engine is registered for
  live sync; today that is `gosync`).

## The gosync engine

### State model — per-file vector clocks

Each peer keeps, per relative file path, a **vector clock**: a map
`{peerID -> counter}`. A local write increments this peer's counter. This
captures causality precisely:

- Incoming clock **dominates** local (>= on every entry, > on at least one):
  remote is strictly newer -> apply remote.
- Local **dominates** incoming: we are newer -> ignore remote (it will catch up).
- **Concurrent** (neither dominates): a true conflict -> resolve (below).

This is strictly better than last-writer-wins: mtime ties and clock skew do not
cause silent overwrites; only genuinely concurrent edits are treated as
conflicts.

Vector clocks + file digests persist in a small sidecar index
(`.bnbloc-sync/state.json` under the dir, itself ignored) so a restart resumes
without a full re-hash.

### Change detection

- Initial: walk the tree, hash files (skip ignored), exchange manifests
  (path -> {clock, digest, size, mode}); reconcile the difference.
- Ongoing: filesystem watch (fsnotify) with a debounced rescan fallback for
  platforms/edge cases. On change, re-hash the file, bump the local clock,
  broadcast the update.

### Efficient content transfer

Content-defined chunking with rolling-hash (rsync-style): the sender offers a
chunk signature list; the receiver requests only missing chunks. Small files
transfer whole. This bounds bandwidth to the actual delta, not the file size.

### Conflict resolution (the higher-order part)

When incoming and local clocks are concurrent for a path:

1. **Common ancestor** is recoverable from the sidecar index (the last digest
   both sides agreed on). With ancestor + local + remote we do a **three-way
   merge**.
2. **Text files** (heuristic: valid UTF-8, no NUL): line-level three-way merge
   (diff3-style). Clean merges apply automatically and the clocks are joined
   (element-wise max, then bump self).
3. **Merge conflict or binary**: write the remote version alongside as
   `path.CONFLICT-<peer>-<ts>` and keep the local file; emit a `ConflictEvent`.
   **No version is ever discarded silently.**
4. Every conflict is surfaced to the room as an activity event (a new
   `sync_conflict` room event, or reuse `agent_messages` with a system role) so
   humans see and resolve it.

### Wire protocol (over the relay raw stream)

Length-prefixed framed messages (all JSON payloads for v1, upgradeable):

- `HELLO {selfID, protocolVersion}`
- `MANIFEST {entries:[{path, clock, digest, size, mode}]}` — initial reconcile
- `NEED {path, chunkHashes:[...]}` / `CHUNKS {path, chunks:[...]}` — delta pull
- `UPDATE {path, clock, mode, digest, chunkPlan}` — a file changed
- `DELETE {path, clock}` — a file removed (tombstone carries a clock too)
- `CONFLICT {path, ...}` — advisory, mirrored to the room

Deletes use clock-carrying tombstones so a delete does not lose to a stale
create and vice-versa (resolved by the same dominate/concurrent rule).

## Bridge integration

- When a room's `syncMode == mutagen` (the "live sync" UI option), the bridge
  for each member: (1) establishes a relay stream keyed by the roomId (the
  sessionKey), (2) constructs the configured `SyncEngine`, (3) runs it against
  the member's project directory for that room.
- The project directory is the agent session's cwd for that room (already local
  and per-member). A room-scoped mapping records it.
- Engine name is resolved from a registry; `gosync` is registered by default.

## Security and safety (implemented)

- **Scope**: only files under the configured project dir are synced; ignore
  rules exclude `.git`, `node_modules`, build outputs, and the sync sidecar, and
  are enforced on **incoming** frames too (`acceptPath`), not just local scan.
- **Authenticated handshake**: HELLO carries a room-scoped token
  `HMAC(BNBLOC_SYNC_SECRET, roomId)`; a peer whose token fails a constant-time
  compare is rejected and the connection aborts. The manifest (file paths +
  digests) is withheld until the peer authenticates, so a wrong-room peer learns
  nothing. Empty secret ⇒ empty token (dev/single-host only).
- **Path traversal rejected**: every peer-supplied path passes `safeRel`, which
  rejects absolute paths, any `..` segment (either separator), and Windows
  drive/UNC prefixes regardless of host OS, before it is joined to the dir.
- **Size caps**: a single synced file is bounded by `maxFileBytes` (32 MiB) and
  one decoded protocol frame by `maxFrameBytes` (64 MiB) via a per-frame limited
  reader, so a peer cannot exhaust memory over the stream.

## Verification plan

- **Single-host pseudo-two-peer** (verifiable here): two directories, two engine
  instances, connected through a real relay stream. Prove: initial reconcile,
  incremental propagation, deletes, and — the key case — a **concurrent edit to
  the same file produces a three-way merge or a `.CONFLICT` file, never silent
  loss**. Unit tests for the vector-clock dominate/concurrent logic and the
  merge.
- **Real cross-machine**: deferred to a two-machine environment; the transport
  (relay raw stream, both peers dialing out) is already proven, so this is a
  deployment step, not new architecture.

## Pluggability payoff

Because the bridge depends only on `SyncEngine`, replacing `gosync` with a
mutagen-backed engine later (in an environment where the mutagen binary runs) is
additive: implement the interface, register it, select it per room. No churn in
the bridge, the room model, or the UI.

## Non-goals (v1)

- Full semantic (AST-aware) merge — line-level three-way is the ceiling for v1.
- Sub-second convergence guarantees — best-effort, debounced.
- Syncing outside the project dir, or multiple dirs per room.

## As-built notes

What shipped, and where it refines the design above:

- **Files**: `services/bridge/sync/engine.go` (interface + registry),
  `services/bridge/sync/gosync.go` (engine), `services/bridge/sync/relayconn.go`
  (`DialRelayStream`, relay raw-stream transport), and per-room wiring in
  `services/bridge/bridge.go` (`maybeStartSync`/`syncPlan`/`runSync`).
- **Authority election**: the merge authority (the peer that runs the 3-way
  merge and announces the result; the other applies it) is the **lower peer id**.
  The peer id is learned from the `HELLO` frame, so neither bridge needs to know
  the other member's id in advance. `cfg.PeerID` is only the pre-HELLO fallback.
- **Session key**: the relay stream is keyed `sync:<roomId>` so it never
  collides with other relay uses of the same room.
- **Project dir**: resolved from `Config.SyncDirFor(roomID)` when set, else the
  bound agent session's `cwd` on the local core.
- **Env overrides** (bridge cmd): `BNBLOC_RELAY_BASE_URL` enables sync (empty
  disables it); `BNBLOC_BRIDGE_SYNC_ENGINE` selects the engine (default `gosync`);
  `BNBLOC_SYNC_SECRET` is the deployment-wide secret from which the per-room auth
  token `HMAC(secret, roomId)` is derived (empty disables auth — dev only).

### Verification (done)

`go test ./services/bridge/sync/` — the suite includes two end-to-end tests that
run the engine **over a real bnbloc-relay raw stream** (`TestSyncOverRealRelay`,
`TestSyncMergeOverRealRelay`): initial reconcile, incremental propagation, and a
concurrent three-way merge converging on both sides. `net.Pipe` was replaced by a
buffered loopback TCP pair in the harness to match the relay's buffered stream.
