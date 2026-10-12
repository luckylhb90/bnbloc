# AGENTS.md

Operational guide for the **bnbloc** repo (`github.com/luckylhb90/bnbloc`), the
multi-user room product where humans and each member's agents collaborate. This
file is distilled from real bugs and workflows hit while building room
participants, agent borrowing, and the desktop client. Read it before changing
the bridge, realtime, activity, shareagent, or the desktop shell.

## Workflow: record before you build

Every bug and every feature/requirement is **recorded before implementation
starts**, then the record is updated as the work lands (root cause / status /
commit hashes).

- **Features / requirements → `docs/prd/`** as `YYYY-MM-DD-<topic>.md`. Small
  ones are a short intent + acceptance note; large ones are a full design doc.
- **Bugs → `docs/bugs/`** as `YYYY-MM-DD-<slug>.md`: repro, root cause (once
  known), fix, and the landing commit(s).

This is mandatory for load-bearing changes: it keeps scope and root cause
explicit and reviewable, and prevents half-remembered scope. Fill what you know
up front (a bug may have a repro before a root cause); update the file, don't
delete it, as understanding and status change.

## Debugging discipline: no temporary patches

When a defect appears, do not patch the symptom with hardcoded values, local
workarounds, one-off flags, stale-state cleanups, or compatibility shims. First
trace the full data/control flow and identify the root cause at the component
boundary where it originates. If the cause reveals an unreasonable design,
fix the design and every affected caller in the same change; remove obsolete
paths instead of leaving fallback branches, duplicated behavior, or garbage
code behind. Every bug fix should leave the system simpler, more observable,
and harder to regress than before.

### MANDATORY: no hardcoded environment data (violations get reverted)

Data that describes the OUTSIDE WORLD (model catalogs, service endpoints,
feature flags, provider permissions, region lists) is NEVER hardcoded as a
default in source. It is fetched from the authoritative source at runtime,
with an explicit, documented config override only when no such source exists.
A hardcoded snapshot is wrong the moment the world changes, and the failure
is invisible: the code looks correct, tests pass, production silently
misbehaves.

- Real incident (2026-09-29, claude-code model catalog): the provider's key
  whitelist was hardcoded from one account's 403 error body. That list is
  per-account, per-channel, and server-managed — the hardcode was stale for
  every other user and undetectable by any test. Replaced by a runtime call
  to the provider's config API using the logged-in identity.
- The rule: **if a value can change without a code deploy, it must not live
  in code.** Env/config override is for operators; the source of truth is
  the authority that OWNS the data.
- Exceptions (a true constant of the protocol, not of a deployment) must be
  argued in the PR/commit message with the owner-of-data named.

For multi-component bugs (client → gateway → service → store → sidecar), gather
evidence at each boundary before proposing a fix: request payloads, signed
identity/tenant headers, persisted rows, event-stream delivery, sidecar logs,
and running artifact versions. A fix is not complete until the exact failing
scenario is reproduced, the source-level cause is fixed, and the same scenario
passes on the real runtime path.

## Development phase: clean cutover, no compatibility cruft

This project is pre-release and has no external users or deployed state to
preserve. **Prefer a clean rewrite/refactor over backward-compatibility.** When
a design changes a setting, env var, DTO, schema, or API:

- Cut over fully: rename/replace the old shape and migrate every caller in the
  same change. Do **not** keep the old field/env/endpoint "for one release",
  add migration shims, dual-read fallbacks, deprecated aliases, or
  version-guard branches.
- Delete dead code paths the change obsoletes; do not leave commented-out or
  "legacy" branches behind.
- No persisted-data migrations for settings/schema — dev stores are disposable;
  reset them (drop tables / clear settings) rather than writing migration code.

If a change ever genuinely needs to preserve external state, that is an
exception to call out explicitly in the design doc and get confirmed — it is not
the default.

## Reuse before you add: no copy-paste subsystems

Recurring smell: a new provider/service/handler is built by **copying an
existing one and tweaking strings**, leaving two near-identical bodies that
drift apart. Before adding code, search for the thing you are about to write and
**reuse or extract** instead of duplicating.

- **Same behavior twice → extract one helper.** If a new file would repeat an
  existing HTTP client, request/response mapping, JSON-path parsing, retry/idle
  loop, or wire-shape struct, factor the shared part into a reusable function or
  type and call it from both. Two `core.Provider`s that both speak the same
  daemon dialect must share one client, not hand-copy `do`/`get`/`post` and the
  session/message decoding.
- **Reuse the existing seam, don't fork it.** New cores go through
  `core.Register` + `coreset.FromEnv` (never a parallel core list); new
  wire/DTO shapes live in `internal/wire`; borrow/authorization goes through
  `internal/shareagent`; SSE/roster through `internal/realtime`. If the seam
  can't express the new case, **extend the seam once** (a shared helper, a new
  interface method, a build-tagged hook) so every caller benefits — do not add a
  second private copy beside it.
- **When a bug is in duplicated code, de-duplicate as part of the fix.** Fixing
  the same bug in N copies is the signal the logic should have been one; collapse
  it rather than patching each copy.
- **A near-copy is only acceptable when the two genuinely diverge** (different
  protocol, different lifecycle). State that divergence in a comment; otherwise
  the default is one implementation.

This is not premature abstraction: extract on the **second** real occurrence,
not in anticipation of one. But once a second occurrence exists, sharing is the
default, not an afterthought.

## One rule, one owner (enforced)

Every rule the system decides on (who the caller is and which tenant they act
in, whether they may see or do something, what is valid, how an id or
version is parsed, what a failure maps to) has **exactly one owner**: one
function or package that all callers go through. A second implementation of
a rule is a defect even when it is correct today, because the copies drift
(2026-10-12: the effective tenant lived in several gateway handlers; only some
honored the acting-tenant switch, and saved content vanished from the
Library).

- **Before writing a check, find its owner** (`git grep` the concept, read the
  "Hard-won traps" below). Use it; if it can't express the case, extend it
  once. Never inline a "simpler" local version.
- **When you find two copies, collapse them in the same change** you are
  making, and delete the loser.
- **Name the owner in AGENTS.md** when it is a cross-cutting rule, and add an
  **architecture test** that fails if the rule is re-implemented elsewhere
  (the pattern of `TestNoSilent5xx`: scan the sources for the forbidden
  construct outside the owner's file). A rule that matters and has no test
  will be re-implemented.
- **Memory and MySQL stores share one contract test suite** per interface; a
  behaviour only one implementation has is a bug in the other. The rig is
  `forEachBackend` (`internal/store/store_contract_test.go`): memory, plus
  MySQL when `BNBLOC_TEST_MYSQL_DSN` is set, with per-run ids (`uniq`).
- **Use a design pattern when it removes duplication or makes a rule
  explicit**, never for its own sake: a registry/strategy for per-kind
  behaviour (`contentpkg.KindSpec`), middleware for per-request policy,
  table-driven definitions for routes and limits, a pipeline of named stages
  for long flows (the drive path). Prefer the smallest construct that leaves
  one place to change.
- **Leave code simpler than you found it.** A change that adds a branch to a
  large function should first extract the part it touches. Delete dead code
  and obsolete paths in the same change.

## Shape

- `services/*` — the control-plane, one Go binary per service, composed by
  `compose.yml`: `room`, `realtime`, `activity`, `shareagent`, `relay`,
  `gateway` (the public edge, `:7090`), and `bridge` (profile-gated, one per
  member). `mysql` is the durable store. No core adapter runs here: adapter
  daemons ship as separately installed Components packages.
- `internal/*` — shared Go: `realtime` (SSE hub), `store` (MySQL + memory),
  `shareagent` (borrow authorization controller), `wire` (event/DTO shapes),
  `roomctx`, `extpipe` (component lifecycle: verify/persist/rollout/audit),
  `pluginruntime` (typed plugin sidecar contract), `extgov` (runtime
  governance guards).
- `core/` — the outbound core seam (`core.Provider`, capabilities via
  `core.As[T]`), `core/manifestcore` (manifest interpreter — a manifest core
  is pure data, installed as a package), `core/protocol` + `core/conformance`
  (Core Protocol v1 client, wire types, conformance suite), `core/pkgmeta`
  (package validation). No concrete core is compiled in.
- `deploy/` — `compose.prod.yml` (prod overlay), `Caddyfile`, `k8s/` (Helm),
  `prod/` (generated per-service + all-in-one + web Dockerfiles),
  `healthcheck/` (the static probe binary in every image), `schema.sql`.
- `web/` — the React SPA (Vite). It is the **same bundle** served by the gateway
  (browser) **and** embedded in the desktop app.
- `apps/desktop` — the Tauri shell: supervises an embedded `bnbloc-bridge`
  sidecar, exposes settings, and loads the web bundle.

## The message path (know this cold)

A room chat and its agent reply flow through several services. Most "no reply"
bugs are a break somewhere on this path, not in authorization:

```
client --chat--> gateway --> realtime (publishes chat_message to the room SSE hub)
bridge (subscribes to the room SSE) --claims by recipient--> local agent core (turn)
bridge --reply--> gateway --> activity (dedup + store) --> realtime hub --> client SSE
```

- **The gateway injects a trusted `X-Bnbloc-User`** from the session token, now
  also HMAC-signed via `svcauth` so internal services can verify it (see the
  svcauth trap below). Backends derive identity from that header, never from a
  request body.
- **The bridge claims by `recipient`, not author.** A chat with
  `recipient={ownerId,provider,core?}` is driven by the bridge that serves that
  agent; `recipient=room` drives nothing (human-only). `core` (e.g. `acme`/`nimbus`)
  picks which local core drives it; absent = the sole/default core.
- **The people chat is invisible to agents, enforced server-side.** A room's
  people chat is a channel with `kind='people'` (one per room, lazily created
  by `POST /v1/rooms/{id}/people-chat`; `GET` reads it without creating it).
  realtime rejects an agent `recipient` there (400) and an `agent_messages`
  ingest into it; an `allChannels=1` stream withholds its events unless the
  subscriber passes `includePeople=1` (the web room view does — the bridge's
  `presence=0&allChannels=1&replay=0` never does, and a failed kind lookup
  withholds, fail-closed); `GET .../channels` never lists it, so the bridge's
  `.room/` history materialization never pages it; plugin hooks
  (`chat_message.v1`) skip it. Edits/deletes/task updates are published
  scoped to the message's channel so their text cannot leak room-wide. Do not
  add an agent-facing read path (listing, stream, history walk) that includes
  it. Inline "People" messages in an ordinary channel are different: no
  agent replies, but agents DO read them as context.
- **Agent replies are attributed to `owner/provider`** (e.g. `jack/codex`), not
  the bare provider. In single-host serve-all mode the reply owner is the
  recipient owner, not the bridge's own owner.

## Hard-won traps (do not relearn these)

### Deploy/rebuild traps

- **`cargo build` alone does NOT rebuild the web bundle or the bridge sidecar.**
  Tauri's `beforeBuildCommand` (web build + `build-bridge-sidecar.mjs`) only runs
  under `tauri build`, not a raw `cargo build --features custom-protocol`. After
  changing web or bridge code you MUST rebuild those artifacts explicitly, then
  the app, or the running app silently uses stale code. Symptom: fixes "don't
  work" in the client while curl against a fresh service works.
- **The web bundle output dir is `services/gateway/webui/static`** (Vite
  `outDir`). Building web repopulates both the gateway's static assets and the
  desktop `frontendDist`. Swapping the gateway binary updates the browser path;
  the desktop app needs its own rebuild to embed the new bundle.
- After a service code change, rebuild that service's container/binary and
  restart it; verify the running artifact hash matches your build before
  concluding a fix failed.
- **All services run `runMigrations` against the shared DB at startup.** Adding a
  column means: put it in BOTH `internal/store/schema.sql` and
  `deploy/schema.sql` (the `CREATE TABLE` for fresh DBs) AND add a migration in
  `internal/store/migrations.go` (the `ALTER` for existing DBs —
  `CREATE TABLE IF NOT EXISTS` never alters). Migration recording is
  serialized with a MySQL
  advisory lock and uses `INSERT IGNORE`; concurrent starters must not crash on a
  duplicate ledger id. Verify: drop `schema_migrations`, restart all services,
  expect the full ledger and zero crashes. `deploy/schema.sql` must stay a
  byte-identical copy of the embedded `internal/store/schema.sql`
  (`scripts/ci.sh go` compares them).
- **`BNBLOC_ENV=production` is the one production switch.** It turns every
  dev fallback (no internal key → trust the header, no OIDC → dev verifier,
  no DSN → memory store, no signing key → ephemeral key, dev-only switches
  like `BNBLOC_ACTIVITY_ALLOW_SERVE_ALL`) into a fatal startup error. The prod
  overlay, the Helm chart, and the all-in-one image set it; a new dev
  fallback must check `config.Production()` the same way.
- **Every deploy surface changes together.** A service/env change touches
  `compose.yml`, `deploy/compose.prod.yml`, `deploy/k8s` (values + template),
  the all-in-one `deploy/prod/aio-entrypoint.sh`, and
  `docs/environment-variables.md` + `docs/deploy-service-env.md` in the same
  change. Document only variables the code reads; delete ones it no longer
  reads.
- **realtime and relay scale out by key ownership (`internal/shard`).** Each
  room (`room:<id>`), member (`member:<id>`) or relay session key is owned by
  one replica, which holds all of that key's in-process state (hub
  subscribers, the durable sequence counter, replay buffer, presence/roster
  and their timers, plugin streams and pending acks, relay pairings).
  `BNBLOC_<SVC>_PEERS` is one `host:port` DNS name (re-resolved every 5 s) or a
  comma list; `BNBLOC_<SVC>_SELF` is `host:port` (or a bare IP in DNS mode).
  Both unset = single node; only one set refuses startup. Every peer is
  probed (`/readyz`) every second and leaves the set after two failures
  (~2 s); a failed call to a live peer (forward or peer request, via the
  set's transport) re-probes it at once, so a crashed replica is dropped in
  ~0.25 s under traffic. Peer calls must use `Set.Client()`/the forward
  transport so failures feed this. Any replica forwards a keyed request one hop
  to the owner (`shard.Forward`; SSE/WebSocket pass through, signed headers
  untouched); `X-Bnbloc-Forwarded-By` means "serve locally, never re-forward"
  and the gateway strips it from clients. Routing keys: `/v1/rooms/{id}/…`
  and `/internal/v1/rooms/{id}/…` → room; `/v1/my/plugin-events` (+ ack) and
  `/internal/v1/members/{m}/…` → member; relay routes → session key;
  store-backed reads anywhere. **Never touch a room's hub from a replica that
  may not own it:** background work goes to the owner (scheduler posts via
  `/internal/v1/rooms/{id}/schedules/{s}/post`, notifications via
  `MemberDesktopRoute`, plugin fan-out via the per-peer hand-off). A new keyed
  route or new per-key in-process state must extend `RouteKey` and
  `reconcileOwnership`; a new peer-to-peer call is svcauth-signed
  (`Peers.call`). More than one realtime replica requires S3 (startup refuses
  the disk store with peers set). Helm runs both as StatefulSets behind a
  headless `bnbloc-<name>-peers` Service.
- **Gateway rate limits are per replica, divided by the live count from
  `BNBLOC_GATEWAY_PEERS`.** Every gateway surface running more than one
  replica must set it, or N replicas allow N times the configured rate.

### Public repo: neutral names only

- **Internal material lives in a separate private overlay, never here.** This
  repo is written public-first: every commit is published as-is. Never add
  internal product or core names, internal hosts, company or person names —
  use neutral names (fixture members like `maya`/`jack`, cores like
  `acme`/`nimbus`, target ids like `local:codex`). Decide the repo before
  writing; if it only makes sense with an internal name, it does not belong
  here. CI runs a secret scan (`scripts/ci.sh secrets`, gitleaks).
- **Public code, tests, compose and docs stay core-agnostic.** Core-specific
  discovery is manifest data (a listener's `defaultPath`/`pathEnv`,
  `hostRewrite`), and the desktop injects no per-core env. Never add a
  core-specific path, env name or default to "make one core work" — extend
  the manifest schema instead.

### Internal identity (svcauth) + room scope

- **Internal services verify `X-Bnbloc-User` via `internal/svcauth`.** The
  gateway (and internal callers — the membership probe, local-agent room calls)
  sign `HMAC(BNBLOC_INTERNAL_KEY, ts+user)`; room/realtime/activity/shareagent
  verify it before any handler. Any NEW internal caller that sets
  `X-Bnbloc-User` MUST also call `svcauth.Sign(req, svcauth.KeyFromEnv(), user)`
  or the target returns 401. Unset key = dev-permissive (loud warning) in
  development, fatal at startup under `BNBLOC_ENV=production`. Symptom
  of a missing Sign: intermittent/total 401 from a backend while the gateway path
  works.
- **A new service that reads `X-Bnbloc-User` MUST wrap its handler in
  `svcauth.Middleware(svcauth.KeyFromEnv(), h)`** in its `cmd/main.go`, and its
  compose entry needs `BNBLOC_INTERNAL_KEY`. A service doing room-membership
  checks also needs `BNBLOC_ROOM_BASE_URL` + a `room` dependency, or
  `membershipFor` falls back to deny-all `NewStatic()` (symptom: owner gets 403).
- **Room membership has one owner: `internal/membership`.** A backend checks
  a request with `membership.RoomScope(ctx, dir, room, user, need)`: fail
  closed (a nil directory refuses), in the room's own tenant (the room
  probe answers it; a request signed for another tenant is refused), with
  the ability the route needs: `AnyMember` (reads, a host's bridge),
  `Human` (`Member.IsHuman`: presence, the people chat, handoff) or `Chat`
  (`Member.MayChat`, human and not a visitor: chat, typing, schedules,
  dispatch requesters, authorizing or borrowing an agent, quota requests).
  Visitors are read-only (D1). `PostChat` enforces `Chat` itself, so no
  post path (scheduler, run-now, dispatch) can skip it. The web mirrors the
  abilities in `lib/roomRoles.ts`, pinned to
  `internal/membership/testdata/abilities.json`. `TestNoMembershipFailOpen`
  forbids fail-open guards, direct directory reads in handlers and visitor
  comparisons outside the owners.
- **A removed member keeps no access.** realtime calls `Directory.Forget`
  when it ingests `member_removed`/`room_deleted` (before delivering it), a
  stream opens on an uncached lookup (`FreshRoomScope`) and re-checks at
  every heartbeat, and the gateway forgets its cached answers on a removal,
  leave or room deletion it proxies (`TestRevocationClosesAccess`).
- **Gateway room scope is dynamic, not just the baked JWT claim.** The session
  token lists rooms as of login; a room created/joined afterward is not in it, so
  `handleProxy` falls back to a live `subjectInRoom` lookup on the claim miss.
  Do not "fix" a post-login 403 by forcing re-login — the fallback handles it.

- **The effective tenant has one rule: `services/gateway/tenant_scope.go`.**
  Session tenant; a super admin's `X-Bnbloc-Acting-Tenant` (the only input:
  there is no `?tenantId=`), ignored for everyone else, applied at
  authentication (`authenticate`, `proxyClaims`), so `claims.Tenant` IS the
  effective tenant in every handler. A request naming a room acts in the
  room's tenant (`rooms.tenant_id`, which wins over the acting tenant) through
  a named variant only: `inRoomTenant` (the proxy, for every path
  `apiPath.namesRoom()`, the room itself included; sub-resources also need
  reach), `roomClaims` (reach: content `?room=`, the job grant, a search
  narrowed to one room, site tickets), `roomEditorClaims`, `roomInTenant`.
  Never read the acting header or call `RoomTenant`/`roomTenantOr` in a
  handler (2026-10-12: the Library ignored the acting tenant while job-grant
  saves landed in the room's tenant); `TestEffectiveTenantSingleOwner`
  enforces it. A gateway call to a backend on a member's behalf signs the
  effective tenant (`svcauth.HeaderTenant` before `svcauth.Sign`), and every
  backend refuses a room request signed for another tenant than the room's
  (`membership.RoomScope`). The web's one owner is `useActingTenant()` in
  `web/src/lib/session.ts` (per gateway and subject, cleared at sign-out; web
  boundary rule 7).
- **A content request's actor has one owner: `contentGrant`**
  (`content_requester.go`). Every content route goes through
  `contentCaller(w, r, write, grant)` and declares its job-grant mode: the
  actions room tools perform honor `?job=&room=` (acting as the job's
  requester), the rest refuse it with 400 (decision O6;
  `TestJobGrantHonoredOrRefused` fails on a route without a mode). The
  artifact download resolves the same actor, so a `?room=` detail's bundle
  URLs download with the same `?room=`.
- **Every session bearer is minted by `mintSession`** (`session.go`): sign-in,
  refresh, fork, tenant switch, API-token exchange, `MintAdminToken`
  (`TestSessionsMintedOnlyByMintSession`). A tenant switch persists on the
  sign-in's refresh family (`store.SwitchRefreshFamily`, the tenant hint), so
  refreshes and forked bridge/CLI sessions land in the switched tenant; the
  web never re-applies it.

### Local-agent cores (multi-core)

- **Multiple cores run at once.** `BNBLOC_CORES` (comma list, e.g.
  `acme,nimbus`) names them; per-core endpoint via
  `BNBLOC_CORE_<NAME>_URL`/`_TOKEN`, else auto-discovered.
  `internal/coreset.FromEnv()` parses this and is the ONE place gateway +
  bridge cmd mains build their env core list — do not re-parse core env
  elsewhere. Every core entry, env-configured or installed (gateway
  activation), is built by `coreset.EntryFor(name, provider, declared)`:
  the `_URL`/`_TOKEN` override, `Explicit` and discovery through
  `coreset.Discover`, the one discovery bound (`DiscoverTimeout`), also used
  by `Rediscover` and the gateway's dev discover route
  (`TestCoreEntriesBuiltOnlyByEntryFor`). The `<NAME>` key is
  `coreset.EnvKey` (non-alphanumerics → `_`); the desktop writes these vars
  only through `core_env_var`/`env_key`, pinned to Go by
  `internal/coreset/testdata/core_env_keys.json` (`core_env_key_matches_go`).
  Unset `BNBLOC_CORES`
  yields no env-configured cores — cores arrive from the extension registry
  (rehydrated installs, activated into the gateway's list) or explicit
  `BNBLOC_CORES` entries, never a baked-in name. The composes now default it EMPTY with no manifests
  mount: install is the ONLY source of cores (see
  `docs/architecture/core-providers.md`).
- **A target id is core-qualified: `<core>/<rawid>`** (e.g. `acme/local:codex`)
  via `core.QualifyTargetID`/`SplitTargetID` (split on FIRST slash; no prefix =
  legacy, core=""). This is the routing key through targets → chat
  `recipient.core` → bridge. Two cores exposing the same harness (`codex`) only
  disambiguate by core, so never drop the prefix.
- **NO core is compiled into the binaries.** Each core takes the adapter form
  its wire contract fits (see `docs/architecture/core-providers.md`):
  - **A manifest core is data** (a JSON manifest, e.g. `acme.json`,
    interpreted by `core/manifestcore`). Listener-descriptor discovery (random
    port+token, a `defaultPath` for a host-side bridge, env-named path
    override, docker host-rewrite that is off unless its env is set),
    composer-options target enrichment, a target prefix (`local:`),
    settled-turn idle predicate (`pollTurn`), the reply reader (`messages`,
    required: no silent reply timeout) and deleteWorkspace op are all
    manifest schema. Distribution is install-only: the manifest is
    package source material — a super-admin installs it
    (`bnbloc cores install --manifest acme.json --name acme` or the platform
    console); the record persists in platform settings, rehydrates at gateway
    boot, and the desktop re-publishes the manifest to its bridge. Nothing
    auto-loads it.
  - **A Core Protocol v1 adapter daemon** hosts a core whose session
    lifecycle is procedural (e.g. pick an online host, create bound to it,
    poll until the runner reports online, then deliver the prompt), which
    manifest data deliberately cannot host. It ships as a Components package
    and forwards the caller's bearer unchanged.
  - **Installed sidecars** speak the protocol directly.
  - **The Core Protocol wire format is the explicit camelCase JSON tags in
    `core/protocol/types.go`.** That file is the contract; every wire field
    carries a tag (an untagged field silently serializes PascalCase;
    `TestWireTypesEveryFieldTagged` parses the file, so non-wire types live
    elsewhere), and every core ↔ wire mapper pair is lossless for a value
    with every field set (`TestWireMappersRoundTrip`).
  - **Every protocol client goes through `core.REST`** (`core/rest.go`):
    `Request` is the one place a bearer goes on the wire, `Send` the one
    bounded read and error mapping (`DecodeEnvelope` keeps each protocol's
    error type), `JSON` the one decode. Manifest cores, the Core Protocol
    and Sandbox Protocol clients and conformance use it
    (`TestProtocolRequestsBuiltByREST`). An envelope's `retryable` is an
    optional `*bool`: absent, `core.Retryable` decides from the status.
  - **Secret-bearing types log through `core.LogRedacted`** (`core/logvalue.go`:
    tokens and secret values redacted, env and headers as names, content as
    size). Every type in `core`, `core/protocol` and `sandbox/protocol` with
    such a field, directly or nested, implements `LogValue` with it;
    `TestSecretsRedacted` derives that set from the sources and logs a fully
    populated value of each.
  - **Optional capabilities go through `core.As[T](ctx, p, ep, cap)`**, never
    a bare type assertion: a manifest or protocol provider implements every
    optional method but `As` consults its `CapabilityReporter` (declared ops /
    discovery document) per endpoint. There are no capability wrapper types
    and no exception: `Discoverer` and `TargetResolver` are capabilities too
    (`discover`, `resolveTarget`; endpoint-independent, asked with a zero
    `Endpoint`). `TestNoBareCapabilityAssertion` scans every Go source.
  - **A request a core does not offer is refused, never dropped, with one
    error:** `core.CapabilityMissingError` (`errors.Is`
    `core.ErrCapabilityMissing`, which a daemon's `capability_missing`
    envelope matches too) from manifest cores and the protocol client alike:
    an absent op or capability, a `Profile` without `agentProfile`, an
    `Execution` without `sandboxExec` (`TestCapabilityMissingReportedAlike`).
  A core with no reachable endpoint is listed but contributes zero targets
  (best-effort skip) — graceful, not an error. Bridge session/lock keys are
  `room\x00core\x00provider` so cores don't collide.
- **A URL-declared adapter is registered for listing, not driving.** When
  `BNBLOC_CORE_<NAME>_URL` names a core nothing else registered,
  `coreset.FromEnv` registers a non-Provider descriptor (like the
  sidecar-package one) so `GET /v1/cores` lists it; `core.New` keeps failing
  and the drive path stays on the explicit-URL branch. Without this the UI
  dead-ends a configured core — the availability-truth rule below.
- **An unknown/unregistered core name is SKIPPED with a warning, not fatal**
  (`coreset.FromEnv`). A typo or not-yet-loaded manifest name in user-editable
  settings must never crash-loop the bridge/gateway — do not "fix" it back to
  fail-fast. `GET /v1/cores` lists every registered core with
  `{available,builtin}` for UI validation.
- **Core availability truth is `GET /v1/cores`, NOT the discover probe.**
  `/v1/local-agent/discover` is dev-gated (404 in production, SEC6), so the web
  MUST NOT use it to decide "is a core connected" — that caused a prod dead-end
  where the Run-agent button stayed disabled even with a core up.
  `useLocalAgent` computes
  `detected = targets>0 || manual URL set || any core available (from /v1/cores)`;
  discover is only an optional dev auto-fill. A zero-core user can
  still join rooms, chat with people, and borrow another member's agent — only
  running their OWN agent needs a local core, so zero-core surfaces show guidance
  (cores are admin-installed: members ask their admin, admins install from the
  platform console or `bnbloc cores install`), never a silent dead button.
- **Runtime component install is live, no restart** (the platform console's
  Components page, or the CLI). `POST /v1/platform/components` (signed
  package body, optional `rollout: auto|optin|off` set atomically with the
  publish) → `extpipe.Install` verifies + decodes + probes + persists the
  record to `platform_settings` (MySQL) + activates it into the extreg registry
  and the gateway's `Cores` list; `DELETE /v1/platform/components/{name}`
  → `Uninstall`. The old `/v1/platform/core-packages` routes are gone —
  `core_packages_test.go` pins them to 404. Trusted keys move under
  `/v1/platform/component-keys`. Every gateway boot re-activates installed
  records (`RehydrateInstalledCores`), so installs survive restarts with no
  env entry — an installed core need not be added to `BNBLOC_CORES`. Installs
  are super-admin-gated and require a trusted key (or dev `--unsigned` with
  `BNBLOC_EXT_ALLOW_UNSIGNED`, which the prod overlay pins off).
- **Plugins are typed sidecar packages, never cores.** A plugin package
  declares `kind: "bnbloc.plugin.package/v1"`,
  `contract: "bnbloc.plugin.contract/v1"`, and `runtime: "sidecar"`, with
  hooks drawn ONLY
  from the code-owned registry (`chat_message.v1`, `agent_messages.v1`,
  `notification.v1` in `core/pkgmeta/package_validation.go`). An unknown hook fails validation
  before anything persists. Realtime is the hook AUTHORITY and AUDITOR, never
  the dialer: plugin sidecars run on member machines, so the server never
  POSTs to a `localUrl` (that was the bug in
  `docs/bugs/2026-10-08-plugin-hook-delivery-unreachable.md`). Delivery is
  PER MEMBER over the member's own authenticated stream: a desktop running
  any plugin sidecar holds `GET /v1/my/plugin-events` (SSE via the gateway),
  realtime sends each `plugin_event` (`wire.PluginDelivery`) to the streams of
  members who are in the event's room and may use the plugin (rollout not
  off, tenant scope admits event tenant + session tenant, opt-in installed),
  the desktop (`internal_ext/plugin_events.rs`) posts the versioned envelope
  `{event, version, tenantId, roomId, payload}` to its loopback sidecar's
  `POST /events` and acks `POST /v1/my/plugin-events/{id}/ack
  {status: delivered|failed}`; realtime audits the ack (or the 60s no-ack
  expiry, or a drop) in `extension_audit` with the member as actor. Two
  members running the same plugin each get the event; an offline desktop
  misses it (no offline queue). The publish path only does a non-blocking
  hand-off to a bounded queue; per-stream buffers and the pending-ack table
  are bounded. Gateway and realtime NEVER import or execute plugin code and
  never launch a process — supervision is exclusively the desktop sidecar
  manager's job (below).
- **Personal delivery is a desktop plugin, never a server-side member
  setting.** `notification.v1` is not a room event: the notification
  emitter (realtime, after prefs/mutes/collapse) hands ONE member's
  notification (`wire.PluginNotification`) to their own plugin streams in
  the event's tenant (`PluginHookDispatcher.DeliverNotification`,
  `notify.DesktopRoute`) and leaves that member out of the server notifier
  items; if no entitled plugin takes it (none running, dropped, ack
  `failed`, 60s no-ack) the `deliveryGroup` fallback queues the server
  delivery for that member (`notify.FallbackRowID`, same event id).
  Producers outside realtime (shareagent borrow requests, gateway alerts)
  have no plugin streams and use the notifiers only. A plugin acts as the
  member only through the desktop **host API**
  (`internal_ext/plugin_host.rs`): packages declaring `permissions.host`
  (`chat` only) get `BNBLOC_PLUGIN_HOST_URL` (127.0.0.1, random port) +
  `BNBLOC_PLUGIN_HOST_TOKEN` (per launch, constant-time compared, revoked on
  exit); `POST /v1/chat` forwards to the gateway chat route with the
  desktop's session (per-plugin rate limit, namespaced `clientMsgId`).
  `localSettings` (plugin packages only) are entered in Settings →
  Notifications via Tauri commands, stored 0600 in the signed-in member's
  own directory (`member_dir`: `<app config>/members/<hash of gateway origin,
  subject and tenant>/`, `internal_ext/member_dir.rs`; never a shared dir),
  passed as
  `BNBLOC_PLUGIN_SETTINGS_FILE`, and NEVER sent to the gateway; a plugin
  missing required settings is not launched, saving restarts it. Do not
  reintroduce member secrets on the server (the old
  `member_notifier_settings` table is dropped by migration).
- **Notifiers are endpoint components; realtime delivers.** A
  `bnbloc.notifier.package/v1` (runtime `endpoint`, contract
  `bnbloc.notifier.contract/v1`) installs through the same shared endpoint
  path as auth-providers (`internal/endpointpkg`: NetGuard + discover probe);
  realtime activates it on reconcile and posts each (event, notifier) outbox
  item signed with the component's install-time secret (shown once,
  rotatable). A notifier is the organization's delivery and has no
  per-member settings (personal delivery: the bullet above). There is no
  tenant webhook — do not add a second delivery seam beside it
  (`docs/notifications.md`).
- **Rollout policy gates every member surface.** An installed record's
  rollout (`extpipe.Record.Rollout`: auto ≡ default/empty, optin, off) is a
  read-time filter applied consistently in `/v1/cores`, `GET /v1/components`,
  and `/v1/cores/{name}/artifact` (the desktop's download path): off →
  hidden; optin → visible only to members with a `component_installs` row
  (`POST/DELETE /v1/components/{name}/install`); auto → everyone. Admins
  switch it via `PUT /v1/platform/components/{name}/rollout`. Opted-in
  components are activated at gateway boot and filtered per member at read
  time. Do not add a member surface that lists installed packages without
  this gate — the desktop sidecar manager follows the member-scoped
  `/v1/components`, so the gate is what keeps an optin sidecar off machines
  that didn't choose it.
- **Component tenant scope is explicit and fail-closed.**
  `PUT /v1/platform/components/{name}/tenants` with an empty list is a 400;
  clearing the scope is `{"allTenants": true}`. Component routes take `?kind=`
  and answer 409 when a name is ambiguous across kinds. There is no
  enable-pin; rollout + tenants are the whole visibility model.
- **`GET /v1/artifacts/{key}` is entitlement-gated.** Super-admins may fetch
  any key; members only artifacts of components visible to them and published
  desktop releases. A new artifact consumer must go through that check.
- **`/v1/local-agent/run` takes a caller-supplied core URL/token only under
  `BNBLOC_DEV_DISCOVERY`** and always requires `agentTargetId`; production
  callers drive the gateway-resolved core endpoint. `BNBLOC_DEV_DISCOVERY` is
  fatal at startup in production (`gateway.CheckProductionEnv`,
  `config.RefuseInProduction`). The route creates a room and drives an
  agent, so it passes the proxy's org RBAC and rate limit (`edgeGate`).
  There is no shared daemon bearer: every gateway request is a session.
- **Gateway-uploaded artifact provenance.** A sidecar package's platform
  binaries are staged through the super-admin `POST /v1/platform/artifacts`
  (streams to realtime's blob store, 256 MiB cap, sha256 content-addressed)
  BEFORE the package is assembled, so the signed envelope pins the hash the
  gateway itself produced. The desktop downloads by that key and refuses to
  run a binary whose sha256 mismatches the signed package. A plugin's
  `sidecar.localUrl` must be loopback http (package validation + probe), and
  the desktop refuses to deliver to a sidecar runtime URL that is not.
- **A digest has one rule: `internal/digest`.** A sha256 is 64 lower-case
  hex characters, a content-addressed key is `sha256/<hex>`
  (`digest.Of`/`Valid`/`Key`/`ParseKey`), compared exactly: an upper-case
  digest is not a digest. `TestDigestRuleSingleOwner` forbids the prefix,
  a 64-hex pattern or a case-blind compare anywhere else. A stored bundle
  (content, previewer, site) is read only through
  `tarbundle.Cache.LoadPinned`, which refuses an empty or mismatching pin
  and caches nothing unverified (`TestBundleLoadsArePinned`).

### Verification traps

- **Verify on the client's real path, not a one-shot curl.** Several bugs only
  appeared with a *persistent* SSE stream + a *reused* session + accumulated
  state. A fresh single curl connection hid them. When testing "no reply",
  open the SSE stream first, then send, and watch both the echo and the reply
  arrive on the same long-lived connection.
- The in-harness headless browser is flaky here (blank `about:blank` pages).
  When it fails, verify behavior through the service HTTP path (the exact
  endpoints the client calls) instead of concluding the product is broken.

### Realtime / SSE

- **The room SSE stream needs a heartbeat.** An idle room with no events sends
  no bytes; the browser `EventSource` / proxies drop the connection on their
  read-idle timeout, the client reconnects (churning presence join/leave), and
  any reply produced during the dead window is missed. Keep the periodic
  `: keep-alive` comment in `realtime` `handleEvents`.
- **Event `Sequence` must be a per-room monotonic counter, never derived from
  the replay-buffer length.** Once the buffer slides past `replayLimit` the
  length plateaus, sequences repeat, and the client (which dedupes by sequence)
  silently drops later events.
- **Only durable kinds (`chat_message`, `agent_messages`) take a sequence.**
  Transient events carry sequence 0 and no SSE `id:`, so the counter always
  equals the stored max and seeding it from the store (restart, ownership
  move) is exact. Two replicas briefly owning a room are fenced by the
  `(room_id, seq)` key plus the idempotent `clientMsgId` retry. Do not
  reintroduce transient sequencing.
- **Durable publishes group-commit per room** (`Hub.PublishCommit`). One
  leader at a time takes what queued during the previous commit, assigns
  consecutive sequences, stores the batch in ONE `AppendRoomMessages`
  insert, and delivers only after it succeeds. Do not go back to a commit
  per event under a room lock (the commit wait then queues every message of
  a busy room), never add an artificial batching delay, and never give the
  commit a timeout or a caller's context: an abandoned insert may still
  commit server-side, reported failed and never delivered. A failed batch
  fails every member and re-seeds; the fence (`ErrDuplicateSeq`) is NOT
  retried locally, so a stale owner stops writing and each caller's
  idempotent retry reaches the real owner. A client id repeated in the
  batch or already in history answers the original sequence with
  `store.ErrDuplicateClientMsg`. Transient kinds bypass the queue (never
  wait on a commit); `member_removed`/`room_deleted` queue behind it.
- **A subscriber that falls a full buffer behind is closed, never skipped.**
  A skipped event leaves no trace on the stream (the client pages history
  only when its stream ends), so a silent skip is message loss; closing
  sends the client down its reconnect-and-catch-up path. Batches arrive as
  bursts, so the SSE handler writes everything queued and flushes once.
- **Presence announces a leave only after a short grace window.** A last-ref
  drop schedules the leave; a re-`Enter` within the window cancels it, so a
  reconnecting client (SSE drop + retry) does not churn `presence_join/leave`.
  Non-presence subscribers pass `?presence=0` (the bridge does) so driving
  agents never inflates or churns the roster.
- **The room feed is persisted for history, but chat is durable ONLY here.**
  realtime persists `chat_message` + `agent_messages` to `room_messages` on
  publish (best-effort; nil store = in-memory dev). `GET .../history?before=<seq>`
  pages older than a sequence cursor. Human chat has no other durable home —
  the in-memory replay buffer (256) is lost on restart, so this is the only
  history source.
- **The hub's per-room sequence must be seeded from the durable max on restart**
  (`Hub.SeedSeq` -> `Chat.MaxRoomSeq`). Otherwise an in-memory reset to 0 re-issues
  sequences below a client's persisted last-read cursor, so post-restart messages
  look already-read (unread stuck at 0) and dedup misfires.
- **Unread = messages with seq > the user's last-read seq** (`room_read_state`).
  `POST .../read` advances it (monotonic); `GET /v1/rooms-unread?rooms=` returns
  per-room counts (membership-checked). The client marks read as the feed's
  latest seq advances.
- **Transient events are NOT persisted.** `presence_typing` (POST .../typing) and
  presence events publish through the hub but are excluded from `persist()` (only
  chat/agent_messages persist), so they never enter history.
- **Safety limits live at two layers.** realtime caps chat text (16KB -> 413,
  empty -> 400, body 64KB via MaxBytesReader); the gateway rate-limits
  cost-bearing writes (POST chat, borrow, room creation) per subject (~2/s,
  burst 10 -> 429). Reads/SSE/history are never limited. The client
  maps 429/413 to specific toasts; other swallowed errors are background
  best-effort by design.

### Bridge / activity

- **A bridge with zero configured cores runs IDLE, it does not exit.** Cores
  exist only after a super-admin installs them (or an operator sets
  `BNBLOC_CORES`/`_URL`), so zero cores at boot is the fresh-install norm:
  presence, sync, and heartbeats keep working while nothing is claimed. The
  desktop's sidecar manager stabilizing-restarts the bridge when the
  gateway-published core set changes, so installs take effect without manual
  restarts. Do not "fix" this back to fail-fast.
- **The desktop owns all sidecar process supervision.** The sidecar manager
  reconciles against the member-scoped `GET /v1/components` (auto or
  member-installed rows only), downloads each artifact by its content key,
  refuses to launch on sha256 mismatch, stops processes whose component was
  uninstalled or flipped to optin/off, and hands the bridge its core list
  (`BNBLOC_CORE_MANIFESTS_DIR` carries the transient manifests; the DB is the
  source of truth). Neither the gateway nor realtime may start, stop, or
  exec a component process — they only deliver signed metadata and events.
  Owners in the shell (`internal_ext/owners_tests.rs` scans for copies):
  uninstall cleanup is `sidecar_sweep` (the shared binary cache, then each
  kind's `KindRuntime::sweep`), run after every successful
  reconcile against the live catalog (never an in-memory diff, which is
  empty after a restart); "this process is our orphan from an earlier
  session" is `owned_orphan` (spawn record from another app pid AND an
  executable under the component's cache root, never either alone); a
  member's state (settings, credentials, sign-ins, sandbox data) lives only
  under `member_dir`, keyed by gateway, member AND tenant, because the
  catalog is: never judge state against a catalog of another scope. The
  sweep takes the member dir from the session the catalog was fetched
  with; only rebuildable machine-wide caches (sidecar binaries, sandbox
  bundles, previewer archives) are swept against whichever catalog is
  current.
- **The desktop shell has one owner per rule** (`owners_tests.rs`,
  `docs/prd/2026-10-12-code-health-remediation.md` Batch 6). Per kind:
  `kind_runtime::runtime(kind)` is the one dispatch; the reconcile,
  supervisor and sweep never branch on a kind (a new kind or a new
  per-kind step extends `KindRuntime`). What a launch hands out (a
  plugin's host grant, the sandbox's launch) lives in its `LaunchGuard`,
  released on every exit path. Processes: every stop goes through
  `child_process::Stopping` and every launch awaits its key's stops (no
  two bridges, no relaunch onto a dying sidecar's port);
  `RestartPolicy::BRIDGE`/`SIDECAR` is the one backoff (a clean bridge exit
  is final, a sidecar always restarts); child output is bounded.
  "Is this sidecar usable" is `ManagedSidecar::endpoint`, and the bridge
  env and its restart watcher read one list (`bridge_cores`). Values:
  `Sha256Pin` (parsed once at catalog decode, every kind),
  `valid_package_name` (every name that becomes a path or route, checked
  at decode), `cache_stem` (`@` separator, injective), `LoopbackHttp`
  plus `loopback::client` (what counts as this machine, no redirects),
  `Secret`/`ct_eq` (random values and their comparison), `Dirs` (every
  app path), `files::atomic_write`/`read_json` (the one state-file writer;
  an unreadable file is moved aside and reported, never reset),
  `AppState::update_settings` (the one settings writer; the web's form
  never reverts the shell-owned sandbox mode), `GatewayAccess::send`
  (every call as the member; 401 is `SessionExpired`, never a refusal or a
  retry-forever; the manager has no session until the web hands one and
  loses it at sign-out), `settings::gateway_url` (the one gateway URL).
- **The desktop owns the sandbox VM, the bridge only uses it.** At most one
  sandbox component (`bnbloc.sandbox.package/v1`) runs per desktop. Its
  executable and declared files live in one sha256-verified per-version
  bundle dir. It gets a fresh loopback port, a per-launch bearer (`tokenEnv`,
  memory only, never logged) and an owner-only data dir in the signed-in
  member's `member_dir` (a member or tenant switch restarts it). Once discover
  answers, the desktop prewarms the one machine `bnbloc` from the package's
  `guest` image. Only `<home>/bnbloc-workspaces` is shared into it, never
  the home dir. Only then does the desktop hand the bridge
  `BNBLOC_SANDBOX_URL/_TOKEN/_MACHINE/_VMEXEC/_WORKSPACES_ROOT`; the
  member's `sandboxMode` goes in `BNBLOC_SANDBOX_MODE` on every bridge
  start. A handoff that appears, disappears or changes mode restarts the
  bridge through the same stabilizing watcher as a core-set change. The
  bridge never creates or deletes machines.
- **Sandboxed drives: the admin token never leaves the bridge.** The bridge
  unsets `BNBLOC_SANDBOX_TOKEN` after reading it and redacts its config in
  logs. Per sandboxed turn, under the drive lock, it re-ensures the space
  (runtime ownership is refreshed only on ensure) and mints a fresh
  space-scoped exec token (`execTokens`). Only that token goes to a core,
  inside `execution`, and only to a core advertising `sandboxExec`. Policy
  (`decideSandbox`): own drives are sandboxed only in mode `all`. Borrowed
  drives are sandboxed when the sandbox is ready and the core supports it.
  Otherwise they run on the host, unless the tenant's
  `requireSandboxForBorrow` is on: then they are refused BEFORE any quota is
  reserved, with a `sandbox-required` notice (job failure `sandbox_required`).
  An unreadable policy refuses too. A failed ensure/mint fails the drive; it
  never falls back to the host. Sessions and locks are keyed by execution
  kind (`driveKey`): switching host↔sandbox starts a new session and
  workspace (created IN the sandbox workdir via `CreateWorkspaceAt`). Nothing
  is moved.
- **Guest commands write the shared workdir past the host deny-write; the
  turn fence covers it** (`services/bridge/sandbox_fence.go`). After every
  sandboxed turn the bridge deletes the space without purge (its processes
  die, its tokens are revoked; the next prepare re-ensures it, quiescing
  first a space it has not quiesced itself and refusing the drive when it
  cannot), then checks the protected paths' fingerprint against the
  session's baseline (taken before a follow-up turn's input, or after a
  fresh session's create); a change drops the session and removes every
  protected path (`resetProtectedPaths`), so no harness starts on config a
  guest wrote (`TestSandboxFence_*`). Never skip the quiesce to keep a
  guest process alive between turns, and never trust a protected path a
  sandboxed turn could have written.
- **`bnbloc-vmexec` confine: write-deny by default.** Only the workspace,
  temp, allowWrite and the listed harnesses' explicit STATE paths are
  writable. Anything that executes on the host later (settings, hooks,
  plugins, skills, MCP config incl. `~/.claude.json`, plugin package caches)
  stays read-only, so a borrowed prompt cannot plant an escape for the owner's
  next unsandboxed session. Do not "fix" a CLI write failure by widening this
  to a whole config dir: add the specific state path to the harness layout.
  A deny-write entry ending in `/` names a directory. Under bubblewrap,
  missing deny-write paths inside writable trees are created as read-only
  placeholders and their parent directories pinned so they cannot be renamed
  away; never skip a deny-write path because it does not exist. Reads of
  bnbloc's own credentials (`~/.bnbloc`, the desktop's data/config/web
  storage) are denied through the one `credentialPaths` list
  (`TestConfineDeniesBnblocCredentialPaths` reads the identifier from
  `tauri.conf.json`). The hook adapters treat only an exact
  `<vmexec> run [--stdio] -- <word>` as already routed and fail closed with
  exit 2 (the PreToolUse code that blocks; any other non-zero lets the tool
  run on the host).
- **A claimed job runs one pipeline of named stages**
  (`services/bridge/drive.go`): admit, resolve content, snapshot
  capabilities, plan execution, authorize, bind target, drive, settle.
  Stages 1–6 fill in the `drive` or return a `refusal`; the core's
  capabilities are asked once (stage 3) and every later stage reads that
  snapshot (`TestDriveCapabilitiesAskedOncePerDrive`). Own, borrowed,
  platform, sandboxed and dispatched drives differ only in the inputs to
  stages 4 and 5: never add a per-kind drive path. A drive slot (session
  and drive lock) is a `driveKey` (room, core, provider, execution kind);
  `TestNoHandRolledAgentRefs` forbids a hand-built key, a `"shared-agent:"`
  literal (use `shareagent.PlatformTarget`) and a core/provider `+"/"+`
  join (use `core.QualifyTargetID`/`SplitTargetID`) in the bridge. A
  content package that cannot be loaded is `missing`, except the expert
  the chat picked (refused).
- **A job retries only when no agent work can have started.**
  `store.AgentJobFailureReason.Retryable` owns what retries
  (`core_unavailable`, `runner_start_failed`, the lease sweep's
  `turn_timeout`); `services/bridge/job_failure.go` owns which reason a
  failure gets and the notice that says why: a stage's `refusal` (a
  `refusalKind`: notice kind + final reason, `invalid_job`,
  `agent_not_advertised`, `content_unavailable`, `team_unsupported`,
  `sandbox_required`, `borrow_not_authorized`) goes through `b.refuse`,
  and a drive error through `b.fail`, classified by the stage it reached
  (`atStage`, `driveFailureReason`): a started or ambiguous turn, and
  `reply_timeout`, are final, or the prompt runs twice and a borrow is
  reserved twice. Every refusal and every drive failure posts exactly one
  notice. Never classify by error text, and never report a failed status
  or declare a refusal elsewhere (`TestDriveRefusalsAreFinal`,
  `TestEveryStageRefusalPostsItsNotice`).
- **Every notice row is a `wire.JobNotice`** (`payload.notice` is its
  kind) under the job's notice session (`wire.JobNoticeSessionID`,
  `wire.IsJobNoticeSession`): refusals, drive failures, the room tools'
  progress lines, sync conflicts. Never post a notice as chat or under a
  real agent session.
- **The bridge MUST send `agentSessionId` when posting a reply to
  `agent-activity/messages`.** Activity dedupes new messages by a
  per-`(room, agentSessionId)` cursor. With the field missing, every session's
  replies collapse onto one empty-key cursor; once that cursor is advanced to
  a large value, all later replies (lower seq) are treated as replay and are
  neither stored nor published — replies vanish with no error.
- **A settled turn is idle even though `activeTurnId` is still set.** Idle
  detection must treat only actively-working phases (`running`/`streaming`/…,
  unsettled) as busy; a turn parked in `waiting`/`paused` is ready for the next
  input. Keying "busy" off any non-empty `activeTurnId` wedges follow-ups
  ("agent still running after 90s").
- **Refresh tokens are server-side families with reuse detection.** Each
  refresh rotates the family; presenting a rotated-away token (beyond a 60 s
  two-tab grace) revokes the whole sign-in. A process handed someone else's
  refresh token (the bridge, from the desktop) must FORK it once
  (`POST /v1/auth/refresh/fork`) and rotate only its own family — never
  rotate the handed token, or both sides fight and the member is signed out.
- **`BNBLOC_BRIDGE_SERVE_ALL_OWNERS` is single-host testing only.** One bridge
  drives every member's agent from one local core. Off in real deployments,
  where each member runs their own bridge. In the desktop app this is the
  persisted "Single-host mode" setting; without it, borrowing another member's
  agent is not driven on one machine.
- **No token-level streaming is available.** The local core exposes no session
  event/stream endpoint (404) and its `/messages` REST returns only `completed`
  messages — never partials. reflow already posts each completed assistant
  message incrementally (multi-message turns stream per-message); sub-message
  streaming would need a new core capability, which belongs to the agent
  runtime, not bnbloc. Do not fake it; the client animates a reveal only.

### Content components, agent profiles and room tools

- **Content components go through one validator.** Experts and skill packs
  (later prompt packs) enter only through `internal/contentpkg` (form,
  archive, import, bridge). A new content kind registers a `KindSpec`; no
  branches in shared code.
- **Archives and directory packing have one owner: `internal/tarbundle`.**
  An upload of a foreign format is read by `ReadArchive` (an unsafe name is
  refused, never rewritten), and every directory pack (save_component's
  draft, the CLI's previewer dir, the built-in packages) goes through
  `PackEntries`/`PackFS` with one exclusion rule (`Entry.Packed`: junk such
  as `.DS_Store`, hidden files, `core.ProtectedWorkspacePath`).
  `TestOnlyTarbundleReadsArchives` forbids `archive/tar|zip` elsewhere
  (the room handoff zip and the sandbox conformance suite are allowlisted).
- **"May member M use component C" has one answer:**
  `extpipe.MemberVisibility`, for executable and content components alike
  (room pins and uses come in through `internal/contentuse`). Never
  re-derive visibility in a handler.
- **Workspace paths have one policy: `core/workspace_paths.go`.**
  `core.BnblocOwnedPath` is what bnbloc writes for the agent (`.room/`,
  `CLAUDE.md`, `AGENTS.md`, `.claude/skills/**`, `.agents/skills/**`,
  `.opencode/**`, `.mcp.json`); `core.HostExecutableProjectConfig` is the
  project config a harness executes on the host (`.claude/**`,
  `CLAUDE.local.md`, `.codex/**`, `opencode.json(c)`), matched
  case-insensitively. Neither is the room's project
  (`core.ProtectedWorkspacePath`): never a deliverable, site content,
  code sync (either direction) or packed component; write-denied in
  sandboxed drives, fenced at every sandboxed turn and cleared from the
  sandbox workdir before a fresh session. A core's file writer accepts only
  `core.WorkspaceFileAllowed` (owned) paths; manifest cores and the protocol
  client refuse any other before the wire (`core.CheckWorkspaceFile`,
  `TestWorkspaceWriterEnforcesPolicy`), and the live conformance suite
  sends one raw to check the daemon refuses it too. Every consumer is checked against one table
  (`core/testdata/workspace_paths.json`, `TestBnblocOwnedPathAgreesEverywhere`
  and its sync/packer siblings); `TestWorkspacePathPolicySingleOwner`
  forbids a local prefix check. A relative path someone else supplies (a
  sync peer, an agent's `dir`, a handoff entry, a core file write) is
  checked only by `core.CleanRelPath` (no `..` segment, not absolute, no
  drive letter; strict callers compare the result with their input);
  `TestCleanRelPathSingleOwner`.
- **Agent profiles are per session.** A profile goes only to a core
  advertising `agentProfile` (`core.Supports`); its
  `core.AgentProfile.BindingSum` (instructions, skill files, the MCP
  servers' non-secret identity) binds the session: a turn whose sum differs
  gets a new one; contributed MCP servers, env and every secret value stay
  out of it.
  Never log profile secrets. Other cores get the prompt-prefix fallback.
  `Target.MCPTransports` says which MCP transports a target accepts (empty =
  stdio only).
- **Someone else's content declaring `scripts` or `mcp.stdio` forces a
  sandboxed drive** (`decideSandbox`), refused where no sandbox can take it.
- **Work on behalf of a running job** (dispatch, content resolution, bundle
  grants) is checked with `store.JobLeasedBy`.
- **Agents dispatch agents only through the room tools, as the job's human
  requester.** `services/bridge/roomtools` is a loopback MCP server, one
  bearer per driven session (constant-time compared, bound to the turn's job
  via `SessionContribution.Turn`, revoked on unbind); the agent never holds
  the bridge session or a gateway credential. `ask_agent` goes through the
  gateway's `POST /v1/rooms/{id}/dispatch`, rewritten and signed onto
  realtime's `/internal/v1/rooms/{id}/dispatch` (room owner replica), which
  posts through `PostChat` as the parent job's human requester with
  `payload.dispatchedBy`, so borrow, quota, budget and sandbox checks apply
  unchanged. Never add a dispatch that authors as the agent or the bridge
  owner, and never bypass the guards (parent live and leased by the caller's
  bridge, requester still a human member, depth <= `wire.MaxDispatchDepth`,
  <= `wire.MaxDispatchFanOut` per parent, no self or cycle, never the people
  chat).
- **An agent reads the room only through `roomlog.History`** (decision
  O1): `.room/`, the pushed context, `read_room` and `ask_agent`'s reply
  poller read exactly the channels realtime's `agentMayReadChannel` admits
  for an agent (`roomlog.AgentChannels`: the room's channel listing, never
  the people chat; a read narrowed to another channel is refused), decoded
  by the one `roomlog.DecodeRow`, which skips notices
  (`TestHistoryReadsOnlyAgentChannels`, `TestDecodeRowSkipsNotices`). The
  bridge reads with its owner's human session, so realtime does not guard
  it: never add an agent-facing history read beside it.

- **Built-in content is a content source**, not a second library:
  `internal/contentbuiltin`, wrapped over the content store at gateway and
  realtime startup, scope `builtin`, artifact key `builtin/<sha256>`.
  Changing a built-in's files means bumping its `component.json` version.
- **Content requests on behalf of a job** (room tools `save_component`,
  `get_component`, …) use the `?job=&room=` grant and act as the job's human
  requester under the dispatch authority rules. Never save content as the
  bridge owner or the agent; a borrowed or platform agent saves no
  script-bearing package.
- **Prompt packs are content, not a library.** Prompts live in a
  `bnbloc.prompt.package/v1` manifest; "Save as prompt" appends to the
  member's `my-prompts` pack; count insertions with `RecordContentPromptUse`
  (once per insertion). Never add a second prompt store or route.
- **A dropped table stays creatable for locked migrations:** when a table
  leaves `schema.sql`, add it to `retiredTables` (`schema_ddl.go`) with the
  dropping migration's id; never edit the locked migration.
- **Teams resolve in two places only:** member experts as the requester in
  `contentuse` (`Effective.team`); member agents staffed only by
  `internal/expertteam` (`POST /v1/rooms/{id}/team-plan`), never on the lead's
  own agent. Team guards (roles, `wire.MaxTeamFailures`, timeout notices,
  `post_progress`) live in the room tools, never in instructions alone. Job
  notice lines go under `job-<id>…` activity sessions with `payload.notice`,
  never a real agent session.
- **MCP servers come only from MCP tool components**
  (`bnbloc.mcp.package/v1`, platform-signed); content names a tool in
  `requires.mcp`, never a command or URL. The desktop downloads stdio tool
  binaries through the shared verified download into
  `BNBLOC_MCP_TOOLS_DIR` and never runs them as services; the bridge attaches
  only an index entry carrying the gateway's pin. Member tool credentials stay
  on the member's desktop (0600) and are used only for their own runs, never a
  borrowed one. Org connectors are sealed with `internal/sealbox` under
  `BNBLOC_SECRET_STORE_KEY` and decrypted only into the per-job resolution for
  a job in that org; no second secret store, no clear-text fallback.
  Unattachable tools go to `contentMissing` and never fail the run.
  One rule each, in `pkgmeta`: a tool's name is `ValidMCPToolName` (a
  package name, never the room tools' `ReservedMCPServerName`, refused at
  install, in `requires.mcp` and in the bridge alike), an env name is
  `ValidEnvName`, "required env is set" is `UnsetEnv` (a blank value is
  unset; connector status and save, job resolution and the bridge agree),
  and a package or content version is `semver.Valid`
  (`TestPackageRulesSingleOwner`).

- **Sharing is a copy, publishing is a second row.** A shared copy is a
  recipient-owned `shared` component (`source_id`, `shared_by`) reusing the
  author's version rows. Trust decisions use `ContentComponent.Author()`, never
  `Owner` (a copy with scripts still forces the sandbox). Org scope is a
  separate row created or advanced only by an approved
  `content_publish_requests` row; never make an author's personal row
  org-visible. Platform content arrives only as a signed package on
  `POST /v1/platform/components` (`contentpkg.PlatformPackage`,
  `Pipeline.VerifyPackage`); the console never signs.
- **"What can member M see" is `contentuse.Visible`** (Library, `name@range`
  resolution, prompt search); don't add another per-scope query list. Content
  has no opt-in rollout: members opt in per use; `retired` is unpublish.

- **Copies go through `internal/contentshare`** (share route and join step
  alike). A room share stands until revoked and is handed out from realtime on
  `member_joined`; any new human-join path in the room service must publish
  `member_joined`.
- **An organization has one content component per kind and name**
  (`ErrContentOrgNameTaken`); references stay `name@range`, never an
  author-qualified syntax. Platform content tenant scope is the component one
  (`PUT /v1/platform/components/{name}/tenants`).
- **A new version of an org-used component reopens approval by itself** (one
  pending per component). Room-tool saves announce themselves with a
  `content_saved` room event; never poll the Library to find a save. MCP
  packages are assembled only by `pkgmeta.PrepareMCP`/`AssembleMCP`.
- **A quick try wears a staged draft, never a component:** `try_draft` stages
  through `POST /v1/content-drafts` (job grant) into `content_drafts`
  (expiring, never listed); only a job dispatched by the staging job, in its
  room and before expiry, wears it. Its trust owner is the staging agent's
  owner.
- **Every agent row a job writes carries `payload.jobId` and
  `payload.sourceSeq`; a dispatch carries `dispatchedBy.parentSeq`.** The feed
  nests delegated work from these alone (`views/room/feedTurns.ts`).
- **The room expert is recorded where it is resolved** (the gateway's
  effective resolution writes `agent_jobs.room_expert_*` and audits a change),
  not at post time.
- **The member stream `GET /v1/my/plugin-events` is the desktop's channel:**
  held whenever the sidecar manager runs, with `?plugins=1` only while a
  plugin sidecar runs (only those get `plugin_event` and count as a desktop).
  A gateway route that changes a member's executable catalog signals
  `componentsChanged` (realtime `POST /internal/v1/components/changed`,
  passed on to every replica); the 60 s poll is only a safety net.
- **http MCP per-member sign-in is `mcp.http.auth.scheme = "oauth"`.** The
  desktop (`internal_ext/mcp_oauth.rs`) discovers the authorization server at
  sign-in (never declared or hardcoded), requires PKCE S256 and checks
  `resource`, `state` and `iss`. Tokens live 0600 under
  `<mcp-settings>/oauth/` in the member's `member_dir`; never send them to the
  gateway, log them, put them in the session checksum or lend them to a
  borrowed or platform drive. Every read, write and delete goes through
  `SignIns` (per-tool lock; a refresh commits only over the sign-in it
  started from, so a sign-out during a refresh wins).

### Borrow authorization

- **Borrowing another member's agent requires that owner's per-room
  authorization; it is fail-closed.** The authorizing owner is always the
  authenticated caller (`X-Bnbloc-User`), never a request-body field — a
  grantee can never self-authorize. Authorization is **per-room** (grant id keys
  on `roomId`) and TTL-bounded.
- The owner authorization surface is the in-room handshake icon (all clients,
  incl. browser owners with no local core), not the desktop-only bridge panel.
- **Borrowed runs are reserved, then settled (R8).** Before driving, the
  owner's bridge POSTs `.../authorization/reserve` (owner-only): ONE
  conditional UPDATE admits the run against liveness + run quota + cost
  allowance (`cost_used < cost_allowance`) and bumps `runs_used`, and the
  run's audit row is written with a `reservation_id` in the same transaction.
  No reservation = no drive (fail closed; the 403 `denyReason` becomes the
  room notice). After the drive the bridge POSTs `.../authorization/record`
  `{reservationId, model?, cost?, tokens?}` to attach usage (cost is added
  even past the allowance — a run cannot be undone; the next reserve is then
  refused). An unsettled reservation expires after 30 min and keeps counting
  as a run. Never move quota enforcement back to record time: concurrent
  borrows then overrun the quota. A serve-all bridge driving another owner's
  agent cannot reserve (owner-only) and uses the read-only authorization
  check, unaccounted — testing mode only.
- **Accountability:** `GET .../shared-agents-usage` projects the owner's
  runsUsed + recent borrowers. A borrow is identified by `actor != owner` (the
  audit `action` column is a fixed enum with no borrow value, so it reuses
  `turn`).
- **Usage budgets (R12) are enforced at the job claim**, not in the bridge:
  activity fails a claimed job `budget_exhausted` (room notice) when the
  owner's — or the tenant's — hard-stop monthly budget is reached. Burn is
  the reported reply usage attributed to the agent's OWNER, so borrowed runs
  count against the owner's budget.
- **`PutGrant`'s upsert must clear `revoked_at_ms` and reset usage** on
  re-authorize, or a re-enabled provider stays revoked (the upsert originally
  omitted these columns — re-authorizing silently did nothing).
- **Platform agents** (`docs/prd/2026-10-10-zero-install-onboarding.md`): a `source=platform` grant is valid only while published (read-time, `liveAuthorization` + reserve) and always sandboxed (reserve `sandboxRequired`); its host's agent-host membership passes `membership.AnyMember` but never `Human` or `Chat` — a new human-only surface checks `RoomScope(..., membership.Human)` (or `Chat`). A platform grant has its own id namespace (`platformGrantID`); `borrowGrant` picks the grant a borrow runs under, so an owner's room authorization of the same agent never overwrites or revokes it. The policy is per host (`hosts[]`; PUT writes only the caller's entry) and a room holds one grant per serving host: decide "is this a usable platform grant / platform drive" only through `internal/shareagent` (`RoomPlatformGrants`, `ReadPlatformDrive`), never by re-reading the policy shape (v2: `docs/prd/2026-10-10-platform-agents-v2-onboarding.md`).

### Rooms, members, messages

- **Owner role is protected everywhere.** Remove-member and role-change both
  refuse to touch the owner (transfer ownership first), refuse self-kick (use
  leave), and add/update-member refuses to grant `owner` (no two-owner rooms).
  `handleRemoveMember` must stay routed — it was implemented but unrouted once.
- **Chat send is idempotent by `clientMsgId`.** The client sends a UUID (retries
  reuse it); realtime returns the original sequence for a seen id instead of
  re-publishing. `room_messages` has a unique `(room_id, client_msg_id)`.
- **Message delete is a soft delete, author-only.** `deleted_at_ms` is set only
  when the requester authored the row (store-enforced); `RoomHistory` excludes
  deleted; realtime broadcasts a transient `message_deleted` (not persisted).
  Owner-moderation delete + edit are deferred.
- **Transient vs persisted:** only `chat_message` and `agent_messages` are
  persisted (`persist()` allowlist). `presence_*`, `message_deleted` and
  `task_updated` (message tasks) publish through the hub but never enter
  history. People-chat messages are persisted like any channel's (history,
  unread, search, handoff export) — agents are kept out by the delivery and
  listing guards (see the message path), not by skipping persistence.
  `message_edited`/`message_deleted` and a task put carry the message's
  `channelId` so their content is filtered like the message itself.
- **JSON column arguments are `json.RawMessage`, never `[]byte`.** The
  MySQL client interpolates parameters (`interpolateParams`, one round trip
  per query); it writes a `[]byte` argument as a `_binary'…'` literal, which
  MySQL refuses for a JSON column (error 3144) and compares byte-wise
  against a text column. Marshal through `store.jsonArg`. The DSN must stay
  utf8mb4 (`store.mysqlConfig` refuses others: client-side escaping is
  unsafe in some multibyte charsets).
- **Subjects are case-sensitive: compare them with `==`, never fold.** User
  ids, owners, authors, actors and requesters are byte-exact everywhere:
  identity columns are `CHARACTER SET utf8mb4 COLLATE utf8mb4_bin` (a new one
  too), Go compares with `==`, and the web uses `sameUser`
  (`web/src/lib/format.ts`, the only copy). `TestNoIdentityFold`,
  `TestIdentityColumnsBinary` and `web/scripts/no-identity-fold.test.mjs`
  fail on a fold; an email address is the one legitimate fold and carries an
  `identity-fold: <reason>` comment (2026-10-12: MySQL's default collation
  let "José" edit "jose"'s message, memory did not).
- **Store reads report failures; login provisioning never upserts a role.**
  A store operation returns an `error`, and a failed read is never "absent"
  (`TestStoreReadsReturnErrors`; its allowlist only shrinks). Login
  provisioning writes through the insert-only `TenantStore.EnsureMember`, so
  a failed or racing read cannot rewrite an org role.
- **MySQL counts matched rows** (`clientFoundRows` in `mysqlConfig`): an
  UPDATE that rewrites the values a row holds is a match. "No row matched
  means not found" is `requireMatched(res, ErrX)`; never re-read to tell a
  no-op from a miss.
- **A key without a tenant is written insert-only.** `artifact_id`,
  `channel_id`, `(room_id, user_id)` carry no tenant, so their writes never
  upsert another tenant's or room's row: `PutArtifact` is INSERT IGNORE +
  read back (`ErrArtifactIDTaken`), `AddMember` inserts from the ctx
  tenant's live room (`ErrRoomNotFound`), `PutChannel` is a plain insert
  (`ErrChannelExists`).
- **A room's rows are listed once: `roomOwnedTables`**
  (`internal/store/room_owned.go`). `DeleteRoom` clears exactly that list on
  MySQL, and every memory store holding room rows implements
  `deleteRoomRows`. A new table with a `room_id` joins it, or
  `roomOwnedExempt` with a reason (`TestRoomOwnedTablesDeletedWithRoom`).
- **Reply usage is read once, at append** (`store.ReplyUsageOf` over
  `wire.TurnUsageFromMetrics`) into typed `agent_messages` usage columns;
  budget burn sums those, never the raw payload (a negative report must not
  lower a burn). A reply is fresh only for the append that stored it (the
  session's `sync_states` row is locked first).
- **A job store error maps in one place** (activity `writeJobStoreError`):
  only `ErrAgentJobLeaseMismatch` is a 409, `ErrAgentJobNotFound` a 404,
  anything else a logged 500 — never "lease lost" for a database blip.
- **Data retention deletes; blob GC deletes what no row references.**
  Retention (`internal/retention`, run by realtime under the
  `bnbloc_retention` advisory lock) purges per tenant and kind through
  `store.RetentionStore`, in batches along a `(tenant_id, <age>)` index. A new
  table holding retained data gets a purge step and an age index there, or is
  explicitly out of scope in the PRD. **Any new column or setting that stores
  a blob key MUST be added to `RetentionStore.ReferencedBlobKeys` (both
  implementations) in the same change**, or the daily blob collection deletes
  the blob once the 7-day grace period passes. Never delete blobs from a
  feature path: content-addressed blobs are shared across rows, and only the
  collection removes them.
- **Never delete a room's max-seq `room_messages` row.** The hub seeds
  sequences from `MaxRoomSeq`; retention redacts that row into a soft-deleted
  `retention_fence` row instead, and any other bulk-delete path must do the
  same. Readers keep skipping soft-deleted rows.
- **One deployment per blob store.** The bucket + key prefix (or the
  attachments volume) belongs to exactly one database; never point a staging
  copy or a restored database at the production prefix.
- **Message search goes through `internal/search`; the store stays the
  authority.** `search.Index` is the only search path (`search.Store` = MySQL
  FULLTEXT ngram, the default; OpenSearch when
  `BNBLOC_SEARCH_OPENSEARCH_URL` is set). OpenSearch returns candidate keys
  with the scope filters in the query; every candidate is re-read from the
  store and kept only if `store.MessageSearch.Admits` passes (the MySQL
  query's own predicates, kept beside it). A new search filter changes the SQL
  and `Admits` together; never return index documents to a client directly.
  Search serves human memberships only (`MemberRooms`); do not add an
  agent-facing search path.
- **Index updates never block or fail a write.** New rows reach an external
  index only through `realtime.Hub.Committed` (room owner, after a successful
  commit); edits and deletes `Enqueue` the key. `search.Updater` re-reads the
  store and writes the current state; failures are logged and counted
  (`search_index_updates_total`) and `bnbloc search reindex` repairs them. A
  new path that writes or changes `room_messages` queues its keys the same
  way. Retention keeps the index in step through `retention.Worker.Search`
  (delete-by-query by tenant and cutoff). A mapping/analyzer change bumps the
  index version in its name and needs a reindex; the analyzer must keep the
  MySQL substring semantics (the parity test pins them).
- **Entity ids come from `internal/ids.New(prefix)`** (prefix + Unix ms +
  80 random bits): InnoDB clusters rows by primary key, so creation-ordered
  ids insert in order while staying unguessable and replica-safe. Never add an
  auto-increment key or a hand-rolled random-hex id helper for an entity;
  secrets, codes and tokens stay fully random. Hot tables key on their access
  path instead (`room_messages (room_id, seq)`: a room's history is one
  contiguous range read).
- **Counts shown as badges are bounded** (`store.MaxUnreadCount`: an inner
  `LIMIT` stops the scan). A new count over an unbounded range must be capped
  the same way.
- **Live schema needs ALTER, not just the schema file.**
  `CREATE TABLE IF NOT EXISTS` does not alter an existing table, so a new
  column (e.g. `client_msg_id`, `deleted_at_ms`) must be `ALTER TABLE`'d
  into the running DB as well.
- **A migration id that ever reached a deployed database is immutable.** The
  ledger records the id, so an edited body never runs there (2026-10-08: the
  outbox gained a column by an in-place edit, production kept the old table
  and every notification enqueue failed). Any schema change after a deploy
  is a NEW migration id, even before "release"; "never deployed" must be
  checked against what was actually deployed, not assumed.
  `internal/store/migrations.lock` enforces it (`TestMigrationLock`, run by
  `scripts/ci.sh go`): it pins each id to a hash of its Go tokens (comments
  and layout are free to change) and each `schema.sql` table to a hash of
  its DDL. Editing a locked body fails ("migration <id> is
  deployed-immutable"); a changed/added/removed `schema.sql` table fails
  unless a not-yet-locked migration names it. Land a new migration with
  `go test ./internal/store -run TestMigrationLock -update` (appends; never
  rewrites a locked id) and commit the lock with it.

### Observability

- Every service exposes `/metrics` (Prometheus text, dependency-free),
  `/healthz`, and `/readyz` via `internal/obs`. `/healthz` is pure liveness
  (always 200 while the process serves — never gate it on a dependency, or a
  DB blip restart-loops pods). `/readyz` is readiness: DB-backed services use
  `obs.ServeWithHealth(..., backends.HealthCheck())` so it pings MySQL (503
  when down); relay's is always 200. Container healthchecks use the static
  `/usr/local/bin/healthcheck <url>` shipped in every distroless image
  (`deploy/healthcheck`). `routeLabel` collapses id-bearing path segments so
  metric labels stay bounded — extend it when adding a new id-bearing route.
- **No silent 5xx.** Every handler 5xx is preceded by
  `obs.ServerError(w, "<what failed>", err)` (or another log call naming the
  branch's error); it rides the request's log line as one Error-level
  `event=bnbloc.http.5xx` entry with `what` + `error`, and the response body
  keeps each service's own shape. `TestNoSilent5xx` (internal/obs) fails on
  a constant-5xx write without it; a status held in a variable is invisible
  to it, so status-mapping helpers call `ServerError` in their 5xx branch. A
  5xx logged with `what=unrecorded` is a missed site. Reverse proxies use
  `obs.ProxyError`. Never put secrets in error strings.
- **Build identity is a build arg, never source.** Images stamp
  `internal/buildinfo` (commit + build time) via `-ldflags -X` from
  `CI_COMMIT_SHA` (`.git` is not in the build context); every internal
  listener serves `GET /version`, and the super-admin
  `GET /v1/platform/versions` / `bnbloc version` / the Platform console's
  Deployment card report skew across replicas and the embedded web bundle.
  A new binary build surface must pass the same ldflags.

### UI

- **`Dialog` caps height at `max-h-[85vh]` with a scrollable body.** A dialog
  with more content than the viewport must scroll its body while header/footer
  stay pinned; do not add dialogs that assume unbounded height.
- This web app uses **plain English toast strings, no i18n layer**. Match the
  existing `useToast()` convention.
- **A URL never carries the session, except the room event stream.**
  EventSource cannot set headers, so `lib/sse.ts` alone sends
  `access_token=` (the gateway accepts it only on `GET …/events`). An
  `<img>`/`<a download>` of an attachment carries a ticket for that one
  attachment (`POST …/attachments/{id}/ticket`,
  `services/gateway/attachment_tickets.go`; cached by
  `lib/attachmentTickets.ts`). Web boundary rules live in
  `web/scripts/check-boundaries.mjs` (run by its node test).

## Concurrent agents in one working tree

When several agents work in one checkout, commit through a private index
(`GIT_INDEX_FILE=<tmp> git read-tree HEAD`, apply only your hunks, commit,
then `git reset -- <your files>`). Never stage in the shared index and wait:
another agent's `git commit` takes it (2026-10-12: one commit swept up another
agent's staged files).

## Toolchain

- **`scripts/ci.sh [go|web|desktop|secrets|all]` is the single CI check
  list** (`.github/workflows/ci.yml` runs one stage per job). Run it before
  pushing; add new checks there, not in the workflow file.
- Go services: `go build ./...`, `go vet ./...`, `go test ./...`. Per-area tests
  live beside the code (`internal/realtime`, `services/shareagent`, …).
  `BNBLOC_TEST_MYSQL_DSN` enables the MySQL integration tests.
- Web: `cd web && pnpm build` (`tsc -b && vite build`); unit tests:
  `node --test 'src/**/*.test.ts' 'scripts/*.test.mjs'` (Node 24).
- Lockfiles are tracked (`web/pnpm-lock.yaml`, `apps/desktop/pnpm-lock.yaml`,
  `apps/desktop/src-tauri/Cargo.lock`, `go.sum`); CI
  installs with `--frozen-lockfile` / `--locked`.
- Images: the root `Dockerfile` (`--build-arg SERVICE=<name>`) is the only
  service Dockerfile; `deploy/prod/<service>/Dockerfile` are generated by
  `deploy/prod/gen-dockerfiles.sh` (CI fails on drift). Base images are pinned
  by version there.
- Desktop app: rebuild sidecar (`node apps/desktop/scripts/build-bridge-sidecar.mjs`)
  and web **before** `cargo build --features custom-protocol` in
  `apps/desktop/src-tauri`.
- Services run via `docker compose` (`compose.yml`); rebuild+restart the changed
  service after a Go change.

## Docs

Feature/requirement records and design docs live in `docs/prd/` (dated files);
bug records live in `docs/bugs/` (dated files). **bnbloc has no fixed
product boundary:** any capability that adds value to the product (agent
orchestration and agent-to-agent collaboration included) may be built, through
the existing seams. Do not reject or narrow a feature by citing a scope
boundary; the earlier "no agent orchestration" decision
(`docs/prd/2026-09-11-agent-collaboration-boundary-decision.md`) is superseded.
The room participants + borrow model is in
`docs/prd/2026-09-11-room-participants-and-borrow-design.md`. Update the matching
doc in the same change when ownership, data flow, or a public contract changes.
