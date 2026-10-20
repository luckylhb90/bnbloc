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

**Design review before building (mandatory).** A PRD for any change that
alters behaviour includes a "Design review" section answering every lens of
`docs/design-review.md` (actors, time and lifecycle, scale and cost, failure
and recovery, boundaries and privacy, abuse, one owner, defaults and
fallbacks, external contracts, load on users and agents, release and
compatibility, observability, AI capability, component or platform) plus a
pre-mortem — checked against the code,
not memory. A bug record includes "Why it was not caught" (the lens, the
missing question or test) and adds that question to `docs/design-review.md`
in the same change.

## Language: English unless told otherwise

Everything the project keeps is written in English: code comments, commit
messages, docs and records (`docs/`, PRDs, bug records, this file), tests and
fixtures, UI strings, and **release notes** (`bnbloc updates publish
--notes`, the Release Hub entry). A conversation with a person is in their
language; what lands in the repo, a release or a deployment is not. Another
language is used only where the task says so (a localized string a feature
exists to produce, a fixture of another language's text), and the file or
commit says why.

## Lessons: the mistake book (LESSONS.md)

`LESSONS.md` is what agents learned the hard way on this project. Read it
before you start; it is short on purpose. It closes the loop of the
workflow above:

- **Review.** After each working day with commits, and after every release,
  run `scripts/lessons-review.sh`. From its digest (commits, reworks, bug
  records, lessons due for promotion), record what was fixed twice, what
  should not have landed as it did, and what was verified too loosely. Each
  finding is an entry, or a new date on an existing one. A bug record's "Why
  it was not caught" and its lesson are the same finding: link them.
- **Promotion.** A lesson seen on three distinct days becomes a rule in its
  owner (a design-review lens, this file, or a check that enforces it), and
  LESSONS.md keeps one line saying where it went.
- **Done right** entries are lessons too: what to keep doing.

## Verification: what "done" means

- **Reproduce the reported case exactly** (its real input, on its real
  surface) before you call a bug fixed or not reproducible. A pass on a
  simplified input proves nothing about the report.
- **Red check for a fix.** The test that covers a bug fails with the fix
  reverted, for the reason the bug names. A test that passes either way is
  not the bug's test.
- **Report what was not run.** A completion report says what was run, on
  which commit and where, and lists every relevant check that was not run
  as "not run" (a platform, a real account, a deploy, a desktop build).
  Never leave one out, and never describe an unrun check as passing.
- **Releases replay real use.** A core or desktop update is checked with
  `bnbloc smoke --scenario long-task --between "<the update>"` on a live
  session (conversation, files and room tools survive), plus
  `scripts/two-member-check.sh` when rooms or borrowing changed.

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

This covers **code paths, never schema steps across a deploy.** A deployed
database is shared by services that restart at different times, so the
previous release's code always runs for a while against the new schema; a
migration only adds, and a drop waits for a later release (expand/contract,
"A migration only adds" below).

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
  the extension registry (`core.Reregister`, from an installed package) +
  `coreset.FromEnv` (never a parallel core list); new
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
- **An architecture test reads the repo only through `internal/archtest`**:
  `archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: …}, fn)` (or
  `archtest.Files`) hands it each source file as a repo-relative slash path
  with its contents. archtest owns what the repo's sources are: the tree
  under `go.mod` without dot directories (git worktrees nested under
  `.claude/` once made a scan read other branches' copies), `node_modules`,
  `target`, `dist`, `testdata` or the embedded web build. A scan picks the
  language (Go, TS, Rust), whether test files count (`Tests`), and its
  sub-roots; a root asked for by name (`.github`) is read.
  `TestRepoScannersUseArchtest` fails on a `filepath.Walk`/`WalkDir`/
  `fs.WalkDir` in a test whose root is not a tree the test made
  (`t.TempDir()`, `os.MkdirTemp`, a `testdata` path, an `fstest.MapFS`).
- **Memory and MySQL stores share one contract test suite** per interface; a
  behaviour only one implementation has is a bug in the other. The rig is
  `forEachBackend` (`internal/store/store_contract_test.go`): memory, plus
  MySQL when `BNBLOC_TEST_MYSQL_DSN` is set, with per-run ids (`uniq`). An
  interface's suite is the test functions named `TestContract<Interface>…`
  (`contract_*_test.go`); `TestEveryStoreInterfaceHasContract` fails on a
  two-backend interface without one, and `TestStoreTestsUseOneRig` on a
  store test that opens MySQL itself (only a schema-altering test takes
  `testmysql.Fresh`).
- **A MySQL test never uses the DSN's database.** `internal/testmysql` owns
  `BNBLOC_TEST_MYSQL_DSN`: `testmysql.Fresh(t)` is a database of the test's
  own, `testmysql.Shared()` one per test binary (the package's `TestMain`
  calls `testmysql.Main`, which drops it). `go test ./...` runs packages in
  parallel, and a shared database made them race each other's migrations
  and claims (`TestMySQLTestsUseTestmysql`). For the same reason an advisory
  lock name is scoped to its database (`advisoryLock`,
  `internal/store/advisory_lock.go`): GET_LOCK names are server-wide.
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
- `component/` — the public contract an installable component built outside
  this module uses (the relay's session, the live view, the fence, the content
  name rule and caps, the sandbox mounts descriptor). Standard library and
  `core` only (`TestComponentContractIsPublic`); additive changes only.
- **No component code here.** Every installable component (MCP tool, plugin
  sidecar, previewer, core adapter, sandbox runtime) lives in its own
  repository: `bnbloc-browser`, `bnbloc-media-tools`, `bnbloc-local-skills`,
  `bnbloc-previewers`, `bnbloc-acp`
  (`docs/prd/2026-10-18-components-in-own-repos.md`). This repository keeps
  the platform, the host side of each contract and the contracts. A component
  needs something from `internal/`? That is a contract: move it to
  `component/` (or `core/`, `web/previewer-sdk`) with one owner, never copy
  it. `TestNoComponentCodeInThePlatform` refuses a component tree or package
  manifest coming back.
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
- **The gateway image builds the SPA from `web/` alone** (plus
  `apps/desktop/package.json` for the version stamp). Production sources never
  import a repo file outside `web/` (boundary rule B1). Tests may read fixtures
  shared with Go (`internal/*/testdata`): the production `tsc -b` excludes
  `*.test.ts`, and `tsconfig.test.json` type-checks them in `scripts/ci.sh web`.
  That stage also rebuilds `web/` in an isolated temp dir, the same layout the
  image sees (`docs/bugs/2026-10-12-gateway-image-web-build-reads-go-fixtures.md`).
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
- **A migration only adds (expand/contract).** Services start, and migrate,
  at different times: during every rollout the previous release's code runs
  against the new schema (2026-10-13: a migration dropped a tenant column in
  the release that stopped reading it, the new gateway could not start, and
  the old one answered every sign-in with 500;
  `docs/bugs/2026-10-13-destructive-migration-broke-mixed-deploy.md`). A
  migration may add tables, columns (nullable, or NOT NULL with a DEFAULT),
  indexes and widenings. Dropping or renaming a table or column, narrowing a
  type, or a column that becomes NOT NULL without a default comes in a
  **later release** than the code that stops using it: release N stops
  reading and writing it (still in `schema.sql`, allowed in
  `writeOnlyColumnAllowed` as retiring; if the previous release does not
  write it, it needs a DEFAULT or NULL first, which is a widening); release
  N+1's migration drops it, `schema.sql` loses it and
  `retiredTables`/`retiredColumns` record it. A rename is add new, ship,
  drop old. A widening that lets new code write values the previous release
  cannot read (NULL into a column it scans as non-null, a new enum value)
  ships its readers first. `TestMigrationsAreDeploySafe`
  (`internal/store/deploy_safe_test.go`) checks every migration newer than
  `migrations.lock`'s `released` marker: a drop passes only when the lock
  recorded the name `unreferenced` (no store SQL of that release named it)
  when the marker was set; a rename never; a column rewrite (`MODIFY`,
  `ensureColumnType`, a dropped default, NOT NULL without a DEFAULT) only
  with a `// deploy-safe: <reason>` comment in the entry. Cutting a release
  runs `go test ./internal/store -run TestMigrationsAreDeploySafe -release`
  on the released commit (it advances `released` to the newest migration
  and records the unreferenced names) and commits the lock.
- **A fatal startup check is for a dangerous or malformed value.** A missing
  production secret, a malformed key, an ambiguous replica set or a value
  that would expose something refuses to start. A well-formed value that
  means the same as unset (a sites origin on the main host, an explicit
  `false` for a dev-only switch, the main listener's own address as the
  metrics address) is accepted, logged when surprising, and never fatal: a
  new image that refuses to start leaves the old one serving against the
  new schema. Dev-only switches are refused through
  `config.RefuseInProduction` only; boolean switches read through
  `config.ParseBool`/`Bool`, or `config.StrictBool` when a malformed value
  must refuse.
- **One release of version skew never returns 500 because of schema.** With
  any service one release behind (or rolled back one release), nothing a
  member does fails on a missing table or column: the rule above guarantees
  it for the store, and a wire change between services follows the same
  expand/contract order. Verify a release on staging before production:
  migrate with the new backends while the gateway stays on the previous
  image, then the reverse; in each state sign in, refresh, open a room,
  chat and run `bnbloc smoke`; `bnbloc version` shows the skew, and the logs
  must hold no `event=bnbloc.http.5xx` naming an unknown column or table
  (`docs/production-deployment.md`, "Upgrade").
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
  assertion is the subject, its tenant (`tenant.Default` when empty), its
  org role and `HMAC(BNBLOC_INTERNAL_KEY, ts+user+tenant+role)`, written only
  by `svcauth.Key.Assert`: an internal call is built with
  `svcauth.NewRequest(ctx, method, url, body, subject, tenant)` (the
  gateway's own calls through `callBackend`, `backend_client.go`), and the
  proxy asserts the session's subject, effective tenant and role on the
  forwarded request with `Assert`. Handlers read the caller with
  `svcauth.Subject(r)` and an org role with `svcauth.Role(r)` (authorize an
  org action with `rbac.Can(svcauth.Role(r), perm)`, never by re-reading the
  tenant store; the gateway drops a non-platform role when a room moves the
  request into another organization). `TestSignedRequestsUseNewRequest` and
  `TestSubjectReadOnlyViaSvcauth` forbid hand-set or hand-read headers (the
  app scope of an interactive app's request too: `Key.AssertApp` writes it,
  `AppScopeOf` reads it). An
  internal route only services call is guarded by
  `svcauth.RequireService(key, services, h)` (realtime publish, blobs, peer
  routes; room agent hosts and share resolution); only the routes that act
  for a member (realtime dispatch, the handoff feed and import) use
  `RequireSigned`. Blob calls assert the calling service
  (`HandoffClient.Service`). Unset key = dev-permissive (loud warning) in
  development, fatal at startup under `BNBLOC_ENV=production`. Symptom of a
  hand-built request: intermittent/total 401 from a backend while the
  gateway path works.
- **A new service that reads `X-Bnbloc-User` MUST wrap its handler in
  `svcauth.Middleware(svcauth.KeyFromEnv(), h)`** in its `cmd/main.go`, and its
  compose entry needs `BNBLOC_INTERNAL_KEY`. A service doing room-membership
  checks also needs `BNBLOC_ROOM_BASE_URL` + a `room` dependency.
- **Every service builds its directory with `membership.FromEnv`** (the room
  service's client; unset `BNBLOC_ROOM_BASE_URL` is fatal in production, an
  announced empty directory that refuses every member in development; never
  a hand-built client or static set: `TestNoMembershipFailOpen`). Realtime
  room routes resolve room, caller and membership only through `s.room` /
  `s.streamRoom` / `s.roomCaller` (`TestRoomPreambleOwned`). A person gains
  a membership only through `addMember` or `redeemInvite` (room_create.go),
  which announce `member_joined` (`TestJoinsAnnounced`). Whether an agent
  may read or write a channel is `store.ChannelAdmitsAgents` (fail closed),
  checked by every agent ingest, the handoff import and activity before it
  stores a reply included (`TestPeopleChatRuleOwned`).
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
  abilities in `lib/roomRoles.ts` (`isHuman`, `mayChat`, `canInvite`,
  `isOwner`), pinned to `internal/membership/testdata/abilities.json` and
  the generated `RoomRolesThatInvite`; the member's own role comes only from
  the room's member list (`useRoomData().me`; the room detail carries no
  role), and a visitor gets a read-only footer, never a composer.
  `TestNoMembershipFailOpen`
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
  effective tenant (`callBackend` → `svcauth.NewRequest`), and every
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
- **Every gateway-served route is a row of the route table**
  (`services/gateway/routes.go`): its org permission (`member` = any
  member, `gated(perm)`), the rate budget a call spends (`costBudget` per
  subject, `guessBudget`/`visitorBudget` per client IP,
  `appReadBudget`/`appWriteBudget` per IP, link and participant), the
  deployment parts it needs (`needContent`, … → 503) or, for a route
  without a session, why (`open`: the credential its handler checks; an
  app route's capability, `serveApp`). `serve` authenticates and
  gates before the handler runs, and the proxy's `edgeGate` uses the same
  `permitted`/`spend`; a handler receives `claims` and never authenticates
  or checks a route permission itself (`TestEveryGatewayRouteGated` walks
  the table as a viewer, an org admin and a member;
  `TestHandlersNeverAuthenticate`). The one suspended-tenant gate is
  `tenantClosed`, the one tenant-membership read `memberRole`, the one
  session-error mapping `writeSessionErr` (suspended/not a member 403,
  rejected refresh 401). The gateway's one live room-membership source is
  `Service.Membership` (`roomMember`, a cached `membership.RoomClient`).
- **Gateway value owners.** Where clients reach the gateway is
  `s.publicOrigin(r)` (`BNBLOC_PUBLIC_URL` when set, else the request's host
  with the scheme a trusted proxy forwarded; `TestForwardedHeadersReadOnce`):
  every link to another member, published artifact URL and previewer CSP.
  A site's URL (a member site ticket, a public link's `siteUrl`) is
  `s.siteURL(path)` only: on the sites origin (`BNBLOC_SITES_ORIGIN`,
  validated at startup by `ParseSitesOrigin`) when set, else the bare path.
  With a sites origin, `withSitesOrigin` (`sites_origin.go`) gates the whole
  mux: site paths (`sitePath`, the one "is this a site path" rule, which
  `appFramePath` builds on) only on that host, nothing else there, 404
  either way; a new site route needs no change, a new API route never
  serves there. A session never goes to a site URL: what reads a site
  with a session (an agent's `read_artifact`) uses the main origin's site
  file read (`handleSiteFileRead`).
  The artifact URL is built and parsed only by `pkgmeta`
  (`GatewayArtifactURL`/`Path`, `ArtifactKeyOf`, `GatewayArtifactKey`;
  `TestArtifactURLOneOwner`). A gateway audit row is written only by
  `s.audit(ctx, tenantID, rec)` over the shared writer, which always takes
  the tenant (`TestAuditWrittenOnlyByAudit`). "Is this component mine" is `ownedBy`
  (owner and tenant; `TestContentOwnershipOneRule`). Environment is read at
  startup (`config.Bool`, `config.List`; `Service.DevDiscovery`,
  `DesktopDownloadURL`, `PublicURL`, `SitesOrigin`), never per request.
- **Every session bearer is minted by `mintSession`** (`session.go`): sign-in,
  refresh, fork, tenant switch, API-token exchange, `MintAdminToken`
  (`TestSessionsMintedOnlyByMintSession`). A tenant switch persists on the
  sign-in's refresh family (`store.SwitchRefreshFamily`, the tenant hint), so
  refreshes and forked bridge/CLI sessions land in the switched tenant; the
  web never re-applies it.
- **Who may open a share link has one owner: `shareAccess`**
  (`services/gateway/share_access.go`,
  `docs/prd/2026-10-13-share-link-sign-in.md`). A link's `access` is `org`
  (the default, `store.shareAccessOf`), `signed_in` or `anyone`
  (`wire.ShareAccessModes`; `wire.ValidShareAccess` is the one validation,
  both store backends included). Which modes an organization allows is
  `store.Tenant.ShareAccessAllowed` alone, over two switches: outside links
  off (`outsideLinksDisabled`) disables `signed_in` and `anyone`, anonymous
  links off (`anonymousLinksDisabled`) disables `anyone`; the room service
  asks it at create, at an access change and at every resolution, and the
  member's `GET /v1/my/tenant-policy` (`wire.TenantPolicy.shareAccess`)
  carries its answer, so the web never reads the switches. The room
  service says whether a link is alive (secret, expiry, revocation, room
  and artifact, and its mode allowed); `shareAccess` says whether this
  caller may open it: `anyone` everyone; `signed_in` any signed-in caller,
  of any organization or none (every session lands in some organization;
  no membership is asked); `org` a signed-in member of the link's
  organization (`memberRole`, `tenantClosed`; not necessarily of the room);
  else 401 `share_sign_in_required` / 403 `share_org_only`. Every share
  route's credential is its `{token}` path segment, opened by `openShare`:
  the link's token (anonymous), or a **share ticket**
  (`auth.KindShareTicket`: member, the link's organization, share id;
  minted only by `POST /v1/share-tickets` after `shareAccess`) in the
  token's place, so a frame, a download link and an app's relative `~app/`
  calls carry it and the sites origin never sees a session. A ticket's
  member is the app's `member:<subject>` participant (never an owner
  outside the room); a visitor token is minted only for an anonymous caller
  of an `anyone` link. A view is counted only after `shareAccess` passes
  (`POST /internal/v1/shares/view`). `TestShareAccessSingleOwner` forbids
  comparing a link's access elsewhere in the gateway and reading the
  switches outside the store and the settings routes.
- **A share link has one owner: `internal/sharelink`**
  (`docs/prd/2026-10-13-short-share-links.md`). The token is 22 base62
  characters (`New`, `Valid`), stored only as `Hash` and found by it
  (`ResolveArtifactShareByToken`, the unique `token_hash`); the share id
  stays the row's key (`ids.New("")`) for tickets and the API. The link is
  `<public origin>/s/<token>` (`URL`): the gateway fills the create
  answer's `url` (`wire.ShareCreated`, `withShareURL` on the proxied
  create, `s.publicOrigin`), and the web shows it and never builds one.
  `GET /s/{token}` (a route-table row) redirects a well-formed token to the
  share page (`PagePath`, the web's `shareHash` in `lib/hashRoute.ts`),
  404 otherwise; it is main-origin only and redacted from request logs. A
  token of another shape (an old `bsh_…` link) is a 404 "share not found"
  without a backend call. `TestShareLinkSpelledOnce` forbids spelling `/s/`
  or `#/share/` anywhere else (Go, and the web outside `hashRoute.ts`).

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
  `{available,backend}` for UI validation (no core is compiled in, so there
  is no "built-in" or "removable" flag, and the registry has no built-in
  source: every extension is installed at runtime).
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
- **Room tools are listed in brief, explained on demand.** A tool's size in
  `tools/list` is paid by every turn of every session, so each tool lists a
  one or two sentence brief with the constraint that must never be missed, its
  parameters, and a note only where a name does not explain itself; the full
  description is `room_help(<tool>)` (`services/bridge/roomtools/brief.go`; a
  tool without a brief is refused). Never put a tool's rules in the session
  guidance, and never add a tool whose brief needs a paragraph. An agent acts
  for the person who asked it only through its job's grant, as that person, on
  routes people already use: `room_setup` reads and changes the room's setup
  through the agent door (`services/gateway/agent_door.go`: the room's
  description only, its welcome and quick prompts, channels, schedules (an AI
  task, a notice, or a request to one of the room's agents online now,
  `agentScheduleTarget`) and group names) and the gateway's grant-honoring
  routes (content uses, sources), the person's role in the room deciding,
  each change audited (`room-setup`) and shown in the room as a line naming
  who asked. Sharing beyond its author is never an agent's act:
  `publish_component` puts a card only the person sees, and their click
  publishes (the gateway refuses a job's `org` publish).
- **Plugins are typed sidecar packages, never cores.** A plugin package
  declares `kind: "bnbloc.plugin.package/v1"`,
  `contract: "bnbloc.plugin.contract/v1"`, and `runtime: "sidecar"`, with
  hooks drawn ONLY
  from the code-owned registry (`chat_message.v1`, `agent_messages.v1`,
  `notification.v1` in `core/pkgmeta/package_validation.go`). An unknown hook fails validation
  before anything persists. A plugin may instead (or also) declare a
  capability that gives it another job and the launch inputs for it:
  `skills-provider` (a bearer the desktop keeps; the skills panel its card
  opens on the Components page renders its `bnbloc.skills.provider/v1` API,
  chosen by the capability, never by a component's name) and `sandbox-mounts` (a descriptor file and a
  mirror folder, `docs/architecture/sandbox-mounts.md`); the package names the
  env vars and a plugin with only those subscribes to no hook
  (`docs/architecture/local-skills.md`). Which kinds may carry which payload field is
  one table (`kindFields`, `core/pkgmeta/shape.go`; a stray field is
  refused, `TestKindOnlyFieldsRefused`), and the runtime blocks are
  `RequireEndpointRuntime`/`requireGuardedEndpoint`/`requireSidecarRuntime`
  (endpointpkg re-applies the first at activation). Realtime is the hook
  AUTHORITY and AUDITOR, never
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
  A plugin's member-scope settings (component settings, below) are entered
  on the Components page via Tauri commands, stored 0600 in the signed-in
  member's own directory (`member_dir`: `<app config>/members/<hash of
  gateway origin, subject and tenant>/`, `internal_ext/member_dir.rs`; never
  a shared dir) and NEVER sent to the gateway; at launch the desktop merges
  them with the platform and organization values it reads from `GET
  /v1/components/{name}/settings/resolved` into the file named by
  `BNBLOC_COMPONENT_SETTINGS_FILE` and each setting's `deliver.env`. A
  plugin with a required setting unset (any scope) is not launched; saving
  restarts it. Do not reintroduce member secrets on the server (the old
  `member_notifier_settings` table is dropped by migration).
- **A component's settings have one declaration, one validator, one store
  and one form** (`docs/prd/2026-10-19-component-settings.md`). The
  manifest's `settings` (plugins and MCP tools) is validated only by
  `core/pkgmeta/settings.go`: keys, types (`text`, `secret`, `url`,
  `number`, `boolean`, `choice`, `path`, `lines`), scopes (`platform`, `org`,
  `member`), defaults, and the env a value is delivered as
  (`ValidSettingEnv`: no variable that changes how a process runs, and
  under `BNBLOC_` only the component's own `OwnEnvPrefix`). A value is
  checked by `CheckSettingValue`, a save by `MergeSettings`, "needs setup"
  is `UnsetSettings` (a default counts), what a component gets is
  `EffectiveSettings`/`SettingsEnv`/`SettingsFile` (a `lines` value is
  stored, sent and delivered in env as its lines joined by "\n", within
  `wire.MaxComponentSettingLines`/`MaxComponentSettingLineBytes`, and is a
  JSON array of strings in the settings file and on a settings page; the
  web's form helper `parseLines`/`linesProblem` is pinned to the same
  fixture); the desktop's copy
  (`internal_ext/component_settings.rs`) is pinned to the same cases
  (`core/pkgmeta/testdata/setting_values.json`). Platform and organization
  values live only in `internal/componentsettings` (sealed per record under
  `component.settings/platform/<kind>/<name>` and
  `component.settings/org/<tenant>/<kind>/<name>`, written by one
  read-modify-write), set through `GET/PUT
  /v1/platform/components/{name}/settings` and `GET/PUT
  /v1/tenant/components/{name}/settings`, every save audited by key, never by
  value; member values never leave the member's desktop. A secret is never
  answered back, only "set". A component's own settings page
  (`settingsPage`, `docs/architecture/settings-pages.md`) is a previewer
  bundle served by the previewer's code under its sandboxed frame; it edits
  one scope only through the host (`web/src/lib/settingsPageFrame.ts`,
  the protocol in `web/previewer-sdk/src/settingsProtocol.ts`), whose saves
  are the scope's own calls. Never give a page a secret's value, a key
  outside its scope or another route to the store; a pin a package carries
  outside `artifacts` joins `Package.Pins`, which the install checks walk.
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
- **An endpoint component holds one credential and asks for its
  settings.** A notifier or an auth provider runs beside the gateway, so
  nothing is delivered to it: its credential (`internal/componentcred`, the
  one owner: created at install, shown once, rotated, deleted; sealed at
  rest with `BNBLOC_SECRET_STORE_KEY`, so realtime, which signs notifier
  deliveries with it, loads the key too) signs its calls to the gateway,
  and `verifyComponentCall` (services/gateway/component_credential.go) is
  the one check of every such route (`TestComponentCallSingleOwner`). It
  reads its platform and organization values with `GET
  /v1/component-settings` (`componentsettings.Store.Endpoint`; no defaults
  filled in, a `version` to poll on). Its declaration has no `deliver` and
  no member scope (`pkgmeta.AsksForSettings`). Never hand an endpoint
  component its values another way, and never add a second signature check
  (`docs/architecture/endpoint-components.md`).
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

- **Room event kinds are registered in `wire.RoomEventKinds`** (durable,
  replay, revocation, channel-scoped). Persistence, sequencing, replay,
  metric labels and stream revocation read it, and the web generates its
  kinds from it. Name the wire constant, never a literal
  (`TestEventKindsRegistered`). The hub replays only replay kinds (the
  durable two), so a reconnect never re-delivers a toast, badge, typing hint
  or stale state (`TestTransientKindsAreNotReplayed`). Read a payload with
  `wire.DecodePayload[T]` (`ChatMessagePayload`, `AgentMessagesPayload`, …),
  never by type-asserting `ev.Payload`. Realtime publishes through
  `s.publish` only, which runs the plugin, notification and member-join
  taps (`TestOnePublishPath`).
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
  A child's environment beyond what its spawner sets is `shell_env`, the
  member's login-shell environment (a Finder/Dock app never runs
  `~/.zshrc`; `docs/bugs/2026-10-13-desktop-agents-miss-shell-env.md`):
  `$SHELL -ilc` prints it NUL-separated between a random marker, once per
  launch (the first spawn waits, bounded by a timeout; a failure falls
  back to the app's own env) and again on "Reload shell environment"
  (`shell_env_reload`, which restarts the bridge and sidecars). Every
  child is spawned by `child_process::spawn(cmd, &shell_env::current())`,
  which sets it beneath the command's explicit env, so `BNBLOC_*`, tokens
  and ports the desktop sets win; `BNBLOC_*` and shell-session names are
  never imported, PATH is the shell's then the app's missing entries, and
  only the count is logged, never a value (`every_child_gets_the_shell_env`).
  Never patch PATH or env for one child elsewhere.
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
  never falls back to the host. What environment crosses into the guest is
  `protocol.ForwardEnvAllowlist(extra)` at both ends (the space
  `envAllowlist`, re-ensured every turn, and vmexec's `ForwardedEnv`):
  the default list plus, only where `decideSandbox` says `ownerEnv` (the
  owner's own drive, no foreign script content), the names the member
  listed (`BNBLOC_SANDBOX_ENV`, rule `pkgmeta.ValidForwardEnv`, the
  desktop's pinned copy `valid_forward_env`). Names only travel; never pass
  the owner's names to a borrowed or platform drive. Workspaces, sessions
  and locks are keyed by execution kind (`driveKey`): switching
  host↔sandbox starts a new session and workspace (created IN the sandbox
  workdir via `CreateWorkspaceAt`). Nothing is moved.
- **Guest commands write the shared workdir past the host deny-write; the
  turn fence covers it** (`services/bridge/sandbox_fence.go`). After every
  sandboxed turn the bridge deletes the space without purge (its processes
  die, its tokens are revoked; the next prepare re-ensures it, quiescing
  first a space it has not quiesced itself and refusing the drive when it
  cannot), then checks the protected paths' fingerprint against the
  workspace's baseline (`workspaceShared.fence`, shared by its channel
  sessions; taken before a follow-up turn's input, or after a fresh
  session's create); a change drops every session of the workspace and
  removes every protected path (`resetProtectedPaths`), so no harness
  starts on config a guest wrote (`TestSandboxFence_*`). A fresh session
  resets the protected paths first only when no other session of the
  workspace is bound (what is there is bnbloc's own, fence-verified, and
  worn by that session). Never skip the quiesce to keep a
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
  run on the host). The vmexec environment is spelled and parsed only in
  `core/protocol/vmexec_env.go` (`SandboxFromEnv`, `HostConfineFromEnv`,
  `PassthroughFromEnv`; `TestVMExecEnvNamesOnlyInProtocol`); a confine list
  in the environment is honored even when the workspace comes by flag. The
  CLIs vmexec knows are one table (`harnesses` in confine.go: layout,
  plugin sources, hook dialect); a sandboxed run's cwd is mapped by
  `runner.guestCwd`, and a host command string runs in `hostShell`
  (`$SHELL`, else `/bin/sh`).
- **A claimed job runs one pipeline of named stages**
  (`services/bridge/drive.go`): admit, resolve content, snapshot
  capabilities, plan execution, authorize, bind target, drive, settle.
  Stages 1–6 fill in the `drive` or return a `refusal`; the core's
  capabilities are asked once (stage 3) and every later stage reads that
  snapshot (`TestDriveCapabilitiesAskedOncePerDrive`). A core that cannot
  say what it offers (`core.CapabilitiesKnown`: a protocol daemon whose
  discovery failed) is not driven in a degraded session without its
  profile, room tools and records: the drive fails `core_unavailable`
  (retried) before anything reaches it. Own, borrowed,
  platform, sandboxed and dispatched drives differ only in the inputs to
  stages 4 and 5: never add a per-kind drive path. A drive slot
  (workspace and drive lock) is a `driveKey` (room, core, provider,
  execution kind); a session is a `sessionKey` (the slot and the channel:
  `docs/prd/2026-10-13-sessions-per-channel.md`), so each channel's
  conversation is its own session's memory, and the slot's lock keeps two
  channels' turns from ever running in one working tree at once;
  `TestNoHandRolledAgentRefs` forbids a hand-built key, a `"shared-agent:"`
  literal (use `shareagent.PlatformTarget`) and a core/provider `+"/"+`
  join (use `core.QualifyTargetID`/`SplitTargetID`) in the bridge. A
  content package that cannot be loaded is `missing`, except the expert
  the chat picked (refused).
- **An agent keeps one workspace; its sessions change**
  (`services/bridge/workspaces.go`,
  `docs/bugs/2026-10-13-agent-loses-its-workspace.md`: a follow-up without
  the expert ran in a new, empty workspace and could not fix the app it
  had built). `agentWorkspaces` is the one owner of which workspace a
  drive slot uses: created for the slot's first session and bound before
  that session starts; every later session (another binding sum, a stale
  session rebound, a core endpoint change, a bridge restart) is created
  in it. It is replaced only when the core answers 404 on the session
  create (`core.NotFound`: the core no longer holds it), and the bound
  workspace is never deleted on a failed create. Bindings persist in
  `BNBLOC_BRIDGE_STATE_DIR` (`workspaces.json`, 0600; the desktop sets the
  member's `member_dir`/bridge); unset, they live in memory. A gone room
  drops its bindings. A deliverable's identity is the agent's, not the
  session's (`artifactIDForFile`: room, owner, core, provider, path), so
  a file edited in a later session publishes a new version of the same
  deliverable. What the sessions of a workspace share at run time is the
  workspace's (`workspaceShared`): the `.room/` tree's pull state (the
  records keep every line of every agent-readable channel, the agent's
  own replies and the current message included: a session's filter would
  hide them from another channel's session), the listing deliverable
  publishing diffs against (and its lock), the sandbox fence. Never
  create a workspace per session again.
- **A job retries only when no agent work can have started.**
  `store.retryBackoffs` is the one retry table: a reason retries
  (`AgentJobFailureReason.Retryable`) exactly when it has a row, and the
  row is its backoff before the next claim (`core_unavailable`,
  `sandbox_starting`, `runner_start_failed`, `budget_unavailable`, the
  lease sweep's `lease_expired`);
  `services/bridge/job_failure.go` owns which reason a
  failure gets and the notice that says why (a sandbox prepare failure that
  means the sandbox is not up yet is `sandbox_starting`: `sandboxStarting`
  over the error and a fresh readiness probe): a stage's `refusal` (a
  `refusalKind`: notice kind + final reason, `invalid_job`,
  `agent_not_advertised`, `content_unavailable`, `team_unsupported`,
  `sandbox_required`, `borrow_not_authorized`) goes through `b.refuse`,
  and a drive error through `b.fail`, classified by the stage it reached
  (`atStage`, `driveFailureReason`): a started or ambiguous turn,
  `reply_timeout` and `agent_error` are final, or the prompt runs twice and
  a borrow is reserved twice. A turn its core reports `failed` fails the job
  `agent_error` even after it posted replies (`agentFailed`): what completed
  with the failure is never posted as a reply, and the core's message rides
  the notice's fenced `detail` (`wire.JobNotice.Detail`). Every refusal and
  every drive failure posts exactly one notice. Never classify by error text, and never report a failed status
  or declare a refusal elsewhere (`TestDriveRefusalsAreFinal`,
  `TestEveryStageRefusalPostsItsNotice`).
- **Every requeue records its attempt, in the statement that requeues.**
  The one requeue is `AgentJob.requeued` (memory) and its SQL twin
  `sqlRequeue` (`internal/store/bridge_jobs.go`): queued, lease released,
  `not_before_ms` set to the retry delay (`retryDelayMs`: the reason's
  base, doubling each attempt, capped; SQL `sqlRetryDelay`, generated from
  the same table), `{attempt, reason, atMs}` appended to
  `agent_jobs.attempts_json` (the newest `MaxRecordedAttempts`; a terminal
  failure keeps them). A bridge's retryable report and the sweep's lapsed
  lease both go through it, under the one rule `retries`
  (`TestJobRequeueOwned` also keeps the backoff table in its owner). The
  sweep owns lease loss: only a queued job past its `not_before_ms` is
  claimable (`claimableJob`/`sqlClaimableJob`, served by
  `idx_agent_jobs_owner_status`), a lapsed lease is requeued as
  `lease_expired` (failed on the last attempt), and a queued job's timeout
  counts from when it became claimable. The activity service's job routes
  and sweep read one clock (`Service.clock`). Members read
  the attempts as reason codes (`wire.AgentJobAttempt`, decision O4) in the
  job events, the room's job list and the run record
  (docs/bugs/2026-10-12-retried-job-hides-why.md).
- **Every notice row is a `wire.JobNotice`** (`internal/wire/job_notice.go`:
  `Payload()` is the one payload with `payload.notice` as its kind,
  `Report()` the one recorded batch) under the job's notice session
  (`wire.JobNoticeSessionID`, `wire.IsJobNoticeSession`): the bridge's
  refusals, drive failures and sync conflicts, activity's swept and
  budget-refused jobs, the room tools' progress and timeout lines. Nothing
  else sets payload.notice (`TestNoticesCarryKind`), and every agent-facing
  reader (roomlog `DecodeRow`, notify) skips notice rows by kind and by
  session. Never post a notice as chat or under a real agent session.
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
- **Deliverables publish once per turn, at settle** (`publishTurn`,
  `services/bridge/drive.go`), after the reply collection ended: each site
  or file the turn changed gets one new version, an unchanged one none.
  Never publish from `reflow` or on a timer while the turn runs: every
  mid-turn pass that saw a rewrite minted a version (2026-10-13: one turn
  published v1–v7 of one site).
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

- **Content components go through one validator.** Experts, skill packs,
  prompt packs and works enter only through `internal/contentpkg` (form,
  archive, import, bridge, a deliverable published as a work). A new
  content kind registers a `KindSpec`; no branches in shared code. A bundle
  is read within `contentpkg.BundleLimits()` (the widest kind's profile)
  until its manifest names the kind; never a fixed profile of one kind. A
  stored version is read against its row's manifest
  (`contentpkg.ValidateStored`): the row is the version's manifest.
- **A work is a content kind** (`bnbloc.work.package/v1`,
  `docs/prd/2026-10-13-works-gallery-and-make-similar.md`): a snapshot of
  one deliverable version of any type (a site's or an app's folder, or the
  one file), never its data. Its `type` has one owner,
  `contentpkg.WorkTypeOf` (an app is a site that is one per the
  projection's `isApp`, an HTML document a site, any other file its media
  type's via `store.ArtifactMIME`), never typed by hand; the validator
  holds the files to the type. `KindSpec.Snapshot` says nothing in it runs
  on a machine (no `scripts` permission, no sandbox forced). Which file is
  a cover and what it may hold has one owner, `tarbundle.SiteCover`
  (sniffed as the type its name says, within `wire.MaxWorkCoverBytes`,
  never an SVG; `TestCoverRuleOwned`); a work's cover order is
  `tarbundle.WorkCover` over it (the cover file, else an image work's
  image, else the generated cover; a video's is the frame its turn saved
  as a cover file, `turnCover`), served only through `writeCover`. A
  version without one gets a **stored cover** beside its bundle
  (`store.ContentVersionCoverStore`, `content_version_covers`, deleted
  with the component; `services/gateway/work_version_cover.go`): any
  reader's render (`PUT …/cover?source=render`, checked by
  `tarbundle.CoverImage`, kept only while the version has no cover) or
  the author's AI cover (`POST …/cover/ai`, which replaces a render); an
  organization row or a shared copy shares its source's (`coverIDs`). The
  cover route answers the bundle's first (`workCoverOf`). The web's one
  maker is `views/gallery/useWorkCover.ts` over `lib/workCovers.ts`, and a
  viewer's drawn cover goes where its host says (`onCover`; a room's
  default is `storeRoomCover`). A
  deliverable becomes a work only through `POST
  /v1/rooms/{roomId}/artifacts/{artifactId}/publish-work`
  (`contentpkg.WorkFromDeliverable`, a room member who may post:
  `deliverableOwner`; the room tools' `save_component` with `artifactId`);
  an app's copy only through `POST /v1/content-components/{id}/use-app`
  (`contentpkg.WorkSite`, a new site artifact, so a fresh empty data
  space; scope `app-copy`). A listing never opens a work's bundle (its
  `coverUrl` may answer 404). Favorites are `store.ContentFavoriteStore`
  (`content_favorites`, per tenant, deleted with the component). "Try
  demo" (an app's) and a site work's preview are only `POST
  /v1/content-components/{id}/versions/{v}/demo`
  (`services/gateway/app_demo.go`): a demo ticket (`auth.KindAppDemo`) in
  the member site path, whose files are `contentpkg.WorkSite` pinned by
  digest and whose `~app/sdk.js` is `appsdk.DemoJS` (the one `sdk.js` with
  its mode argument `"demo"`: an in-memory data space of the page, nothing
  sent). A demo ticket names no room or app: never let the app data API
  (`ticketApp`) or any `forwardApp` path accept it
  (`TestAppDemoNeverReachesAppData` counts the backends' app calls).
- **A work's recipe has one owner: `deliverableRecipe`**
  (`services/gateway/work_recipe.go`): the producing job through
  `roomlog.ProducerOf` (the one producer rule; no job, no recipe), its
  request text and expert, the packs in effect (`contentuse.Effective`,
  the room's and the requester's prompt packs), its attachments within
  `wire.MaxWorkReferences`/`MaxWorkReferenceBytes`, the agent. It is
  captured at publish and shown first (`GET …/artifacts/{id}/recipe`);
  the request's `recipe` overrides it (references only a subset of the
  captured ones) and `includeRecipe: false` drops it; the reference files
  ride the bundle under `references/` (`contentpkg.WorkReferences`), never
  a copy of the site. "Make similar" is only `POST
  /v1/content-components/{id}/make-similar`
  (`services/gateway/make_similar.go`): the room as for a copy
  (`galleryRoom`), the references copied in through realtime's own upload
  as the caller's attachments, the packs they may see turned on through
  `putContentUse` (the one writer of a content use, the room's editors
  only), the expert when `contentuse.Usable`, the rest reported in
  `skipped`; an empty recipe (`contentpkg.RecipeEmpty`) attaches the work
  itself; scope `make-similar` counts it.
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
  (room pins and uses come in through `internal/contentuse`);
  `MemberVisibilityWith` when the opt-in install is read per component.
  "Does record R reach tenant T" (events, notifier targets) is
  `extpipe.TenantDistribution`. Never branch on a rollout or call
  `AllowsTenant` outside extpipe (`TestVisibilityOwned`). In the gateway a
  component route's `{name}` resolves only through `requestComponent` (the
  409 ambiguity rule, the "name required" check) and a tenant scope
  validates only through `setTenantsRequest.scope`. What the member
  catalog lists (`GET /v1/components`, and the components an assist's
  `capabilities` context names) is `memberComponents.listed`.
- **The shared audit trail has one writer: `internal/audit.Record`** (best
  effort, a failed write logged as `bnbloc.audit.append_failed`); outcomes
  come only from `wire.AuditOutcome*` (`TestAuditThroughOneWriter`).
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
  gets a new one, in the agent's same workspace; contributed MCP servers,
  env and every secret value stay out of it. Every session on an
  `agentProfile` core gets the room tools, with or without content (a
  turn wearing nothing still has a profile: the guidance and the
  contributors' servers).
  Never log profile secrets. Other cores get the prompt-prefix fallback.
  `Target.MCPTransports` says which MCP transports a target accepts (empty =
  stdio only).
- **Someone else's content declaring `scripts` or `mcp.stdio` forces a
  sandboxed drive** (`decideSandbox`), refused where no sandbox can take it.
- **Work on behalf of a running job** (dispatch, content resolution, bundle
  grants, a team plan) goes through `store.LoadLeasedJob` and its one status
  mapping `store.LeasedJobStatus` (404 not in the room, 409 not running, 403
  another member's bridge, 500 a store error).
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
- **Model-facing text has one owner per category**
  (`docs/architecture/room-tools.md`, "What every agent is told";
  `docs/bugs/2026-10-13-model-facing-text-review.md`). Session guidance is
  `roomlog.SessionGuidance(roomlog.Guidance)`, one `go:embed` template
  (`internal/roomlog/guidance.tmpl`) rendered with the session's facts
  (`Self`, `HasTools`, `HasRoomFiles`, `TeamLead`; numbers from their
  constants, never literals): who the agent is, the records and `[now]`
  rules, the room model, the reply language, the `.room/` legend, the room
  tools' usage and the team default rule. The bridge's `sessionGuidance`
  is the one owner of where it goes and of its facts: the head of the agent
  profile on an `agentProfile` core (outside the binding sum), else the
  head of the session's first prompt (an empty room's too); a session
  contribution declares `RoomTools` and words nothing, and the room tools'
  MCP `instructions` is one line pointing to it. Scaffolding is
  `internal/roomlog/markers.go`: `RecordsHeader`, `Fence(author, text)`
  (every text someone else wrote that a tool or file hands an agent: a
  reply, `read_room`, another author's component, a deliverable, the live
  trail, team role descriptions), `Line` (attributed record lines), the
  `[now — from <sender>]` marker and `Truncate` (the one rune-safe cut);
  `TestModelTextMarkersSingleOwner` forbids their literals and the
  identity line in any other Go string. Never put static guidance in a
  per-turn prompt: `ComposePointer` is the message alone (plus one records
  line on a core without a profile). Every surface has a golden, and
  `TestPromptBudgets` (`services/bridge/prompt_test.go`) bounds each one.
  `.room/README.md` (`ReadmeFile`, the session's guidance) is written only
  when its content changes (`ReadmeUpdate`); `.room/artifacts.md` (each
  deliverable's producer by `VersionProducer`, the source job's agent from
  the run record, marked not in this workspace) and `.room/library.md`
  (`ReadLibrary`: `GET /v1/components?job=&room=`, the requester's Library
  as the gateway's visibility owners admit it; the room tools'
  `list_components` reads the same) and `.room/apps.md` (`ReadApps`, the
  room's interactive apps with their collections' counts, written by
  `AppsUpdate` once the room has an app and every turn after, so it never
  goes stale; `CtxState.AppsListed`). An agent with tools reads a
  deliverable with `read_artifact`, through the room's own artifact routes
  (membership and room checked there), bounded and fenced as untrusted; a
  site's file through the gateway's site file read on the main origin
  (`GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{v}/files/{path...}?job=`,
  `site_file_read.go`: the job grant, the room service's read as the
  requester, 4 MiB a file), never a site ticket or site URL
  (`TestRoomToolsNeverOpenSiteTickets`).
  `.room/deliverables/<id>/` (`roomlog.CopyDeliverables`, the pull pass,
  before the records that name them) holds a read-only copy of each
  listed deliverable's latest version (a site as its file tree, anything
  else as one file), so an agent recovers any deliverable's source
  whatever happened to its workspace, and an agent without tools reads
  other agents' work. A copy is written only when its version changed
  since the workspace's last pass (`roomlog.CopyState`, kept with the
  workspace in `agentWorkspaces`, cleared with a reset `.room/`), within
  `MaxDeliverableCopyBytes` per deliverable and
  `MaxDeliverableCopyPassBytes` per pass; `artifacts.md` says where each
  copy is or why there is none. To edit one, the agent copies it into
  its own working tree (the guidance says so). Never tell an agent a
  deliverable or a component is one of its working files, and never
  compute a producer, a Library listing or an app listing elsewhere.

- **Built-in content is a content source**, not a second library:
  `internal/contentbuiltin`, wrapped over the content store at gateway and
  realtime startup, scope `builtin`, artifact key `builtin/<sha256>`.
  Changing a built-in's files means bumping its `component.json` version.
  A built-in prompt text lives only in its package: the web reads the
  builder's quick prompts (`workshopDraft`) and the starters through the
  content API, never a copy (`TestBuiltinPromptsStayOutOfTheWeb`).
- **The validator's bounds are one table, `contentpkg.Limits()`.** Text
  that states a limit renders it from there: the builder's
  `package-format` skill (`TestBuilderLimitsMatchValidator`, `-update`
  rewrites its table). The builder's always-on `INSTRUCTIONS.md` stays
  under 1,500 tokens (`TestBuilderAlwaysOnBudget`): what it needs only
  while drafting goes into that skill. Built-in text names no workspace
  path (core's lists own them) and no language.
- **Every starter prompt is a prompt pack's** (`starterChoices`): the
  room's packs' starters, then the built-in packs' (`starters`). Never add
  a starter list of the web's own, nor of any other record: a room
  package requires prompt packs (`requires.prompts`), which a room made
  from it gets as room content uses (resolved for the installer).
  `TestOneStarterMechanism` fails on a starter list in Go, the schema or
  the web.
- **A room's setup is a content kind, `bnbloc.room.package/v1`**
  (`contentpkg/room.go`; docs/prd/2026-10-20-rooms-as-components.md,
  docs/architecture/content-components.md "Room packages"); room
  templates are gone. Its schedules are AI tasks, notices to the room and
  requests to an agent without the agent (never one that posts as a
  person; `contentpkg.RoomScheduleMisfit` and `PackageRecipient`), checked
  by the schedule rules' owners (`schedule.CheckText`, `schedule.Validate`);
  a request installs waiting for the installer's agent, and a schedule
  waiting for one (`schedule.WaitsForAgent`) is never enabled or run
  (realtime's `runnable`, the one place);
  its `sources` are asks with a purpose (`wire.RoomSourceAsk`, a name of
  the one list `wire.RoomSources`), recorded for the room at install and
  granted only by the organization admin (below). A room's draft is only
  `GET /v1/rooms/{roomId}/component-draft` (its owner or an admin) and is
  saved through the content form like every kind; a room is made from one
  only by `POST /v1/content-components/{id}/create-room`
  (`services/gateway/room_components.go`), which goes through the
  backends' own routes as the installer (`POST /v1/rooms`, channels,
  realtime's schedules created disabled), `putContentUse` and
  `setRoomExpert` (the one writer of a room's expert) and `copyApp` (the
  one copy of an app, shared with "Use this app"), and deletes the room
  when a step fails once it exists. A `name@range` reference resolves for
  a member only through `contentuse.Resolve`. A package also carries the
  room's `INSTRUCTIONS.md` (never beside `requires.expert`: install makes
  them a private expert of the installer's, the room's expert), apps made
  in the room (`appSnapshots`, sites under `apps/`) and ticked room files
  (`files/`), within `wire.MaxRoomCarryBytes` together
  (`contentpkg/room_carry.go`; docs/prd/2026-10-20-complete-room-capture.md).
  A save takes them from the room only through its `include`, read as the
  saver by `roomIncluded` over `currentDeliverable` (the client never
  sends their bytes); install restores them through `copySite` and
  `createRoomArtifact`, the gateway's one creation of a deliverable from
  bytes. The tools a room's runs had come from the run records' `tools`
  (`wire.RunDiagTools`, what the bridge attached to the session, never
  the calls made), read through activity's room route.
- **A room reads organization records only through its consent:
  `services/gateway/room_sources.go`** (docs/prd/2026-10-20-rooms-as-components.md
  §2.3, §7). An installed room package's asks (`room_source_grants`) wait
  until the organization admin grants a scope (`wire.RoomSourceScopes`) or
  withdraws it, audited (`wire.AuditKindSource`); `sourceCover.covers` is
  the one rule of which rooms a grant reaches (never the reading room; a
  room of unknown visibility is private; `opted-in` reads the owner's
  opt-ins, `room_source_opt_ins`). A grant is only for a private reading
  room and is pinned to its owner (`grantee`); `watchGrant` is the one
  check that it still holds, run on a successful change the edge proxies
  (`apiPath.changesSourceRoom`, `afterSourceRoomChange`), before every
  read (`heldGrant`) and when the console opens the ask: the room made
  shared withdraws it, a new owner pauses it, a new member and a
  grantee's task created or changed since the consent are told to the
  admins (the latter with the built-in `explain-source-use` on the
  grantee's budget, shown until an admin acknowledges); only
  `room_sources.go` changes a grant. A schedule reads only through the
  schedule-run tool `read_records` (`schedule_records.go`, never an agent
  tool), within the grant as `heldGrant` answers it at each call, for a
  schedule of the grantee, bounded and audited (range and count, never a
  record);
  names reach only an organization admin's schedule (`readerSeesNames`),
  everyone else reads the ask's aliases (`sourceAlias`). A record's turn
  (the request's file names, the run's last reply) is read through the
  gateway's one history read, `roomHistory` (`assist.go`), as a consented
  read: realtime's gateway-only `GET /internal/v1/rooms/{id}/history`, the
  members' history handler without the membership, never the people chat.
  Every gateway read of a room's history goes through it. Members of a
  covered room see which room reviews them (`GET /v1/rooms/{id}/sources`).
  A note to an organization's admins goes through `notifyOrgAdmins`
  (room reports and source asks alike). A run's reply, usage and tools summary
  in its diagnostics is read only by `store.RunDiag`, a run page's cursor
  only by `store.AgentRunCursor`/`ParseAgentRunCursor`.
  `TestRoomSourceConsentSingleOwner` keeps grants, scopes, the gateway's
  run reads and its history reads in their owners.
- **Content requests on behalf of a job** (room tools `save_component`,
  `get_component`, …) use the `?job=&room=` grant and act as the job's human
  requester under the dispatch authority rules. Never save content as the
  bridge owner or the agent; a borrowed or platform agent saves no
  script-bearing package.
- **Prompt packs are content, not a library.** Prompts live in a
  `bnbloc.prompt.package/v1` manifest; "Save as prompt" appends to the
  member's `my-prompts` pack; count insertions with `RecordContentPromptUse`
  (once per insertion). Never add a second prompt store or route.
- **A use count is one organization's.** Built-in and platform content is
  used by every organization, so every use-count write and read takes the
  caller's effective tenant (`RecordContentPromptUse`,
  `ContentPromptUses`, `CountContentUses`, `CountMemberUses`; an empty
  tenant is `tenant.ErrNoTenant`), and `content_prompt_uses` keys on it.
  Never mark a count's SQL `cross-tenant`;
  `TestContractContentComponentStoreTenantUseCounts` checks organization
  A's uses never show in organization B's counts.
- **A dropped table stays creatable for locked migrations:** when a table
  leaves `schema.sql`, add it to `retiredTables` (`schema_ddl.go`) with the
  dropping migration's id; never edit the locked migration. A dropped column
  goes to `retiredColumns` the same way (the migration helpers skip it, and
  any index over it, when a locked migration names it on a fresh database).
- **Teams resolve in two places only:** member experts as the requester in
  `contentuse` (`Effective.team`); member agents staffed only by
  `internal/expertteam` (`POST /v1/rooms/{id}/team-plan`), never on the lead's
  own agent. Team guards (roles, `wire.MaxTeamFailures`, timeout notices,
  `post_progress`) live in the room tools, never in instructions alone.
  The lead's roster section (`expertteam.Section`) carries content only,
  never staffing: who takes a role now is `list_room_agents`' `team`, so a
  role agent coming or going never changes the lead's binding sum.
- A failed job's member-facing sentence is
  `store.AgentJobFailureReason.Text`; the web's one reason table is
  `AGENT_JOB_FAILURES` (`views/room/agentJobLabels.ts`: a label and a
  "why" clause per reason, read through `failureLabel`/`failureWhy`; a live
  retried job's "retrying — …" is `retryNote`). A share's notification is
  `contentshare.Announce` (one dedup key per member and version), from the
  gateway and realtime alike.
- **MCP servers come only from MCP tool components**
  (`bnbloc.mcp.package/v1`, platform-signed); content names a tool in
  `requires.mcp`, never a command or URL. The desktop downloads stdio tool
  binaries through the shared verified download into
  `BNBLOC_MCP_TOOLS_DIR` and never runs them as services; the bridge attaches
  only an index entry carrying the gateway's pin. A tool's settings
  (component settings, above) reach it as its env and settings file
  (stdio) or its headers (http). Member values stay on the member's desktop
  (0600) and are used only for their own runs, never a borrowed one.
  Platform and organization values are sealed with `internal/sealbox` under
  `BNBLOC_SECRET_STORE_KEY` and decrypted only into the per-job resolution
  for a job in that org (and a member's resolved read of a component their
  desktop runs); no second secret store, no clear-text fallback. A tool
  that needs setup is not offered to the agent. Unattachable tools go to
  `contentMissing` and never fail the run.
  One rule each, in `pkgmeta`: a tool's name is `ValidMCPToolName` (a
  package name, never the room tools' `ReservedMCPServerName`, refused at
  install, in `requires.mcp` and in the bridge alike), an env name is
  `ValidEnvName`, "a required setting is set" is `UnsetSettings` (a blank
  value is unset; the settings forms, job resolution, the bridge and the
  desktop agree), and a package or content version is `semver.Valid`
  (`TestPackageRulesSingleOwner`).

- **Sharing is a copy, publishing is a second row.** A shared copy is a
  recipient-owned `shared` component (`source_id`, `shared_by`) reusing the
  author's version rows. Trust decisions use `ContentComponent.Author()`, never
  `Owner` (a copy with scripts still forces the sandbox). Org scope is a
  separate row created or advanced only by an approved
  `content_publish_requests` row, or by `publishToOrg` where the
  organization requires no approval; never make an author's personal row
  org-visible.
- **Whether a publish to the organization waits for an admin has one
  owner: `contentshare.NeedsApproval(tenant)`** (the tenant settings'
  `publishApproval`, `required` by default, `none` publishes at once,
  still audited and removable by admins; every kind). The gateway's one
  publish path is `publishToOrg` (the publish route, a member's click on
  a `publish_component` card included, a new version's resubmission); a
  job's grant never gets through it (`errJobWidens`: neither a publish
  nor a saved version's resubmission, which the room tools turn into the
  member's publish card); it alone opens a request
  (`openPublishRequest`) or publishes (`orgPublish`, beside an admin's
  approval), and nothing else reads the setting
  (`TestOrgPublishDecidedByNeedsApproval`). Platform content arrives only as a signed package on
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
- **Interactive apps' data has one owner per rule**
  (`docs/prd/2026-10-12-interactive-apps.md`). Each published site (an
  app; `app_id` = the site artifact's id) owns a data space of JSON
  documents. Storage is `store.AppDocs` (`app_docs` keyed `(app_id,
  collection, doc_id)`, `app_keys`): every query names app, tenant and
  room; an insert is create-only per id (the primary key decides `ifAbsent`)
  and needs the app's current key; `UpdateAppDoc` is the one write (`SELECT
  … FOR UPDATE`, the caller's callback, `version + 1`). Encryption is
  `appdata.Cipher` alone (a per-app data key wrapped by `internal/sealbox`,
  AES-256-GCM bodies bound to `app_id|collection|doc_id`, the sealed
  author, the `_by` pseudonym; `Rotate` re-wraps keys only):
  `TestSealedColumnsWrittenOnlyByCipher` forbids setting `BodySealed` /
  `AuthorSealed` or naming the sealed columns elsewhere. Deleting data
  crypto-shreds: the key goes first (an empty `wrapped_key` refuses
  writes), then the rows in batches (`WipeApp`; `DeleteRoom`'s
  `roomOwnedTable.destroy`/`batched`). The document rules are
  `internal/appdata` (server stamps `_id _by _createdAt _updatedAt
  _version`, `_author` for owners only, never client-set: `ParseBody`;
  `Body.Patch` for merge and `$inc`; the filter whitelist `FilterOps`,
  applied after decryption). The room service's data owner
  (`services/room/appdata.go`) holds the two platform rules: owners are the
  room's members who may chat and its agents (`appOwner`); only the author
  or an owner changes a document (`mayChange`); private unless written
  public (`readable`). Its routes (`/internal/v1/app/…`) are gateway-only
  (`RequireService`) and take the whole scope from the signed app scope:
  the gateway asserts identity (`Assert`, the room's tenant) and then
  `svcauth.Key.AssertApp` (room, app, `<kind>:<id>` participant: `member`,
  `agent`, `visitor`) under one timestamp; the handler reads it only through
  `svcauth.AppScopeOf`. No route takes an app, room or tenant from the
  path, query or body (`TestAppDataScopeFromCapabilityOnly`). `If-Match:
  <version>` is `ifVersion`, `If-None-Match: *` is `ifAbsent` (both 409);
  caps are the `wire.MaxApp*` constants; every status goes through
  `writeAppError`. Every write announces a transient `app_changed`
  (`wire.AppChanged`, metadata only; `announceAppChange`). Reading data
  out is audited by one writer and one rule
  (`services/room/appdata_export.go`): `auditAppRead` writes one
  `internal/audit.Record` row per call (kind `wire.AuditKindApp`, never
  per document) when `appReadAudited` says so: every export, and every
  agent read of documents (`find`, `get`); a member's or visitor's own
  reads through the app are not. The owners' export
  (`GET …/apps/{artifactId}/export?collection=&format=json|csv`) streams
  from the room service; the CSV form (columns, quoting, defused formulas,
  nested JSON) is `internal/appdata` export.go alone, never the web's.
- **An app request's scope has one owner at the edge:
  `appCapabilityOf`** (`services/gateway/apps.go`). The `~app` API serves
  under three entry points, each a route-table row per call (`appRoutes`):
  a member's site ticket (`/v1/sites/{ticket}/~app/…`, Kind `member`), a
  share link (`/v1/public/shares/{token}/site/~app/…`: a share ticket in
  the token's place, Kind `member`; or an `anyone` link's token plus its
  visitor token, Kind `visitor`, the `X-Bnbloc-Visitor` header, never the
  URL) and a session in the room of the path (`/v1/rooms/{roomId}/apps/{artifactId}/…`; with `?job=` the
  job's agent through `jobGrant`, the content routes' one admission).
  Nothing comes from the query (but `job`) or the body
  (`TestAppCapabilityReadsNoScopeInput`); a share link is resolved on
  every call (a dead link refuses at once); `forwardApp` is the one call
  to `/internal/v1/app/…`. A visitor token is the `auth.KindAppVisitor`
  kind, bound to the share id, minted only by `handleAppVisitor`. The
  change feed is realtime's `/internal/v1/rooms/{id}/app-events` (the
  room's `app_changed` only) narrowed to the app by `appEventsOf`. The
  SDK (`services/gateway/appsdk`, `go:embed`) spells the wire constants
  the share page's handshake uses (`wire.AppVisitorRequest`/`Grant`),
  pinned by `TestAppSDKServed`; it is one file with one transport switch:
  its mode argument (the last line, `"live"`), which `appsdk.DemoJS`
  sets to `"demo"` for an app's demo (never a second SDK file); app budgets are `appRates` (IP, link,
  participant; writes stricter). A frame entry's `~app/` answers CORS for
  any origin without credentials (`appFramePath`). On the web, a public
  link to a site is the app full-screen (`SharedApp`), and the share page
  is the only holder of the visitor token (`lib/appVisitor.ts`: answers
  only its own frame's window, renews before expiry); a member's site
  viewer answers no visitor request. The web never picks a site's host:
  `siteUrl` is the gateway's (`siteGatewayUrl` resolves a path, keeps an
  absolute URL). The desktop frames a member site through its
  `bnbloc-site` scheme only (`site_scheme.rs`), which forwards to where
  `GET /v1/public/client-config` says sites are, passes every `~app/`
  method with its body and the app API's request headers, and holds the
  change feed itself, answering it as batches (a scheme responder takes
  one body): never admit a gateway or sites origin in the shell's CSP.
- **An agent reads an app's data only through the room tool `app_data`**
  (`services/bridge/roomtools/appdata.go`: list, find, get, insert, put,
  patch, delete, and wipe with `confirm`), on the gateway's room entry
  with the bound job's `?job=` (the agent is an owner; the room service's
  rules apply unchanged). Its path is spelled only by `roomlog.AppPath`;
  the room's apps for an agent are `roomlog.ReadApps` (the listed
  deliverables the projection marks `isApp`, each with its owners' `stats`),
  read by `.room/apps.md` and `app_data list` alike. Every document it
  returns is fenced (`roomlog.Fence`) and bounded (`maxAppDataText`, a find
  cut there continues with `next`). The bridge's gateway client carries a
  write's conditions as `RawBody.Header` (`If-Match`, `If-None-Match`).
  The built-in `interactive-apps` skill pack documents the SDK; its SDK
  section is pinned to `sdk.d.ts` (`TestInteractiveAppsSkillMatchesSDK`:
  a renamed or added SDK method fails until the skill follows), the
  `app-builder` expert requires it, and `app-starters` holds `/poll`,
  `/survey`, `/signup`, `/app-report`. Before an agent finishes an app the
  skill makes it test the page in a headless browser (no console errors,
  a visible main view, the main action written and read back) and never
  call it done when a check failed or could not run
  (`docs/prd/2026-10-13-interactive-apps-self-test.md`); the harness is
  its `references/self-test.md`, and the SDK it serves is the demo-mode
  one bnbloc writes into every pull workspace as `.room/app-sdk/sdk.js`
  (`roomlog.AppSDKUpdate` over `appsdk.DemoJS`, rewritten only when it
  changes; never a second SDK file).
- **"Is this deliverable an app" has one owner: `store.Artifact.IsApp`**
  (`internal/store/artifact_apps.go`, `docs/prd/2026-10-13-app-gallery.md`
  §5): a site whose data space exists (its `app_keys` row, not being
  destroyed) or whose current version loads the app SDK. `sqlArtifactIsApp`
  is its SQL twin beside it; every read fills the two facts
  (`LoadsAppSDK`, `HasAppData`), and the projection's `isApp`
  (`Artifact.View`) is the only way out: the deliverables' Apps filter
  (`?app=1`, `ArtifactQuery.Apps`), a member's apps (`ListApps`), the
  agents' `ReadApps` and the web read it, never re-derive it. Whether a
  version loads the SDK is detected ONCE, while its bytes stream into the
  blob store (`putVersionBlob` tees a site bundle through
  `appdata.SDKScan`; `appdata.SDKPath` is the SDK's one spelling) and
  stored on the version (`artifact_versions.loads_app_sdk`): no listing
  ever reads a bundle back (`TestAppRuleSingleOwner`,
  `TestSiteAppSDKDetectedOncePerVersion`). A member's apps across rooms
  are `GET /v1/my/apps` (`services/room/my_apps.go`): the rooms of the
  signed tenant where the caller may chat (`Member.MayChat`, the app owner
  rule), keyset-paged, each with its room's name, its producer
  (`roomlog.ProducerOf`, the one producer rule, over the run store), its
  document count (only with a data space) and its working links. An app's
  cover is `GET …/versions/{v}/cover` (`services/gateway/site_cover.go`):
  `cover.png`/`.webp`/`.jpg` at the site's root, served only when its bytes
  sniff as that type and within `wire.MaxWorkCoverBytes`, with nosniff and a
  sandbox CSP (never an SVG); without one the web draws `lib/appCover.ts`.
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
- **A grant is in force by one rule: `store.Grant.Live(atMs)`.** The
  policy-aware form is shareagent's `usableGrant` (Live, and a platform
  grant only while published), read by `liveAuthorization` and every
  listing; `admitReservation` is the one admission rule (used + 1 run <=
  quota, settled cost below the allowance). Reserve-then-settle is the only
  borrow authority: there are no leases and no start/turn/release routes
  (`TestGrantLivenessOwned`).
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
  count against the owner's budget. The hard stop fails closed: an
  unreadable budget or usage refuses the job with the retryable
  `budget_unavailable` (a logged server error), and a refusal that cannot be
  recorded keeps the job off the bridge; never run a job unbudgeted
  (`TestBudgetHardStopFailsClosed`).
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
  (`TestStoreReadsReturnErrors`; its allowlist only shrinks).
  `TestMySQLReadsReportFailures` calls every operation of every MySQL store
  on a closed database and requires the error. A MySQL list read goes
  through `queryAll` (`internal/store/mysql.go`, one `rowScanner` scan
  func), which returns query, scan and `rows.Err()` failures; a single-row
  lookup maps only `sql.ErrNoRows` to found=false. Login
  provisioning writes through the insert-only `TenantStore.EnsureMember`, so
  a failed or racing read cannot rewrite an org role.
- **The store's small rules live once, in `internal/store/rules.go`:**
  page sizes (`clampLimit(n, def, max)`, same call on both backends),
  expiry (`liveAt`: live strictly before `expires_at_ms`; SQL `> ?`), the
  duplicate-key mapping (`isDupEntry`/`isDupKey`/`dupAs` to an exported
  sentinel both backends return; nothing else reads a driver error),
  NULL writes (`nullIfZero`, `nullPtr`) and the `sqlNotDeleted` /
  `sqlHumanMember` predicates. `TestStoreRulesSingleSource` fails on a copy
  outside its owner (bridge liveness and job-status IN lists included).
- **Every SQL on a tenant table names `tenant_id`, or says why not.** A
  store function reading or changing a table with a `tenant_id` column
  without filtering on it carries a `// cross-tenant: <reason>` comment (a
  claim across tenants, a global token, a retention pass);
  `TestTenantTablesFilterTenant` fails otherwise.
- **MySQL counts matched rows** (`clientFoundRows` in `mysqlConfig`): an
  UPDATE that rewrites the values a row holds is a match. "No row matched
  means not found" is `requireMatched(res, ErrX)`; never re-read to tell a
  no-op from a miss.
- **Bridge liveness and job statuses are spelled once**
  (`internal/store/bridge_jobs.go`). Go asks `BridgeLive(hb, nowMs)` and
  `AgentJobStatus.Terminal/Live/Pending`; SQL uses `liveBridgePred` and the
  IN-lists generated from the same status slices (`sqlTerminalJobStatuses`,
  …); a store takes `nowMs` from its caller, never the clock, to decide
  liveness. Job and bridge ids are global: claim, status, lease and the
  owner-wide reads are cross-tenant, and `BridgeOwner` is the one answer to
  "whose bridge is this" (`TestStoreRulesSingleSource`).
- **A key without a tenant is written insert-only.** `artifact_id`,
  `channel_id`, `(room_id, user_id)` carry no tenant, so their writes never
  upsert another tenant's or room's row: `PutArtifact` is INSERT IGNORE +
  read back (`ErrArtifactIDTaken`), `AddMember` inserts from the ctx
  tenant's live room (`ErrRoomNotFound`), `PutChannel` is a plain insert
  (`ErrChannelExists`).
- **An SSO issuer maps at most one tenant** (`uq_tenant_issuer`; no issuer
  is NULL, never `''`). `PutTenant` refuses a taken issuer with
  `store.ErrSSOIssuerTaken` on both backends (the console answers 409);
  it is update-else-insert, because `ON DUPLICATE KEY UPDATE` on a table
  with a second unique key updates whichever row clashed.
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
  explicitly out of scope in the PRD. **Any new column that stores a blob key
  MUST join `blobKeySources` (`internal/store/retention.go`, the one list both
  `ReferencedBlobKeys` implementations read) in the same change**
  (`TestBlobKeyColumnsReferenced` fails on a `*blob_key` column missing
  there); a platform setting is scanned for `sha256/<hex>` keys. Otherwise
  the daily blob collection deletes the blob once the 7-day grace period
  passes. Never delete blobs from a feature path: content-addressed blobs are
  shared across rows, and only the collection removes them.
- **A platform setting is read-modify-written through
  `store.UpdatePlatformSetting`** (compare-and-set over
  `PlatformSettingsStore.SwapPlatformSetting`, re-applied when a concurrent
  writer won), never `Get` then `Put`: two hosts saving the platform-agent
  policy at once used to lose one entry (`store.UpdatePlatformAgents`).
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
- **Counts shown as badges are bounded** (`store.MaxUnreadCount` for unread,
  `store.MaxUsageCount` for the Library usage counts: an inner `LIMIT` stops
  the scan, per component). A new count over an unbounded range must be
  capped the same way.
- **A stored column is a read column.** Every `schema.sql` column is read
  by some store query, or sits in `writeOnlyColumnAllowed` with a reason
  (`TestNoWriteOnlyColumns`, a static check over the store's SQL literals).
  A column nothing reads is dropped in a new migration, not kept "for later",
  once a release without its reads has shipped ("A migration only adds").
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
  rewrites a locked id) and commit the lock with it. Immutable is not
  deploy-safe: a new migration also obeys "A migration only adds"
  (`TestMigrationsAreDeploySafe`).

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
  A secret that travels in a path (an invite code, a relay key, a site or
  previewer ticket, a share token) is redacted from the request log by
  `secretPathRes` (`internal/obs/obs.go`): a new capability-in-the-path
  route joins it. A new credential goes in a header, never the URL.
- **One error body, one decoder: `internal/httpx`.** The gateway and every
  backend answer errors with `{"error": "<message>"}` (`httpx.ErrorBody`,
  plus a `code` where a route defines one, e.g. dispatch refusals) through
  `httpx.Error` (http.Error's argument order), and decode JSON bodies with
  `httpx.DecodeJSON(w, r, limit, &v)` (413 oversized, 400 malformed,
  everywhere). A dependency the deployment does not configure is
  `httpx.Unavailable(w, what)` → 503 (never 500/501/502). Go clients read a
  failed body with `httpx.ErrorText`; the web's only reader is
  `errorDetail` (`lib/session.ts`). `TestOneErrorBodyOwner` forbids
  `http.Error`, private `writeJSON`/`writeErr` pairs and hand-rolled request
  decoders outside the owner.
- **No silent 5xx.** Every handler 5xx is preceded by
  `obs.ServerError(w, "<what failed>", err)` naming the branch's error (a
  bare slog/log line does not count); it rides the request's log line as one
  Error-level `event=bnbloc.http.5xx` entry with `what` + `error`.
  `TestNoSilent5xx` (internal/obs) fails on a constant-5xx write without it.
  A status computed at run time (a mapping helper's result, an upstream's
  status) is written only by `httpx.WriteStatus(w, status, msg, err)`, which
  records the cause of any 5xx; an upstream status is never passed through
  as-is (`TestErrorStatusesThroughWriteStatus`). A 5xx logged with
  `what=unrecorded` is a missed site. Reverse proxies use `obs.ProxyError`.
  Never put secrets in error strings.
- **Build identity is a build arg, never source.** Images stamp
  `internal/buildinfo` (commit + build time) via `-ldflags -X` from
  `CI_COMMIT_SHA` (`.git` is not in the build context); every internal
  listener serves `GET /version`, and the super-admin
  `GET /v1/platform/versions` / `bnbloc version` / the Platform console's
  Deployment card report skew across replicas and the embedded web bundle.
  A new binary build surface must pass the same ldflags.

### UI

- **The web's server shapes are generated from Go, never hand-mirrored.**
  `internal/wire/cmd/wiregen` writes `web/src/lib/wire.gen.ts` (regenerate:
  `go generate ./internal/wire`; `scripts/ci.sh go` fails on drift). A JSON
  shape the web reads is a struct in `internal/wire`, never a
  `map[string]any` or a store row: members see `store.AgentJob.View()`,
  `Artifact.View()` and `ArtifactVersion.View()` (decision O4: no tenant,
  lease, raw diagnostics or blob key; `TestProjectionsOmitServerFields`;
  a run record's raw diagnostics reach only the agent's owner and org
  admins by signed role, `runViewer.seesDiagnostics`). A
  value set is a named string type, a `//wire:union <Name>` const group, or
  `//wire:union <Name> <Field>` on a registry variable (the union of each
  row's field: `RoomEventKind` from `wire.RoomEventKinds`); a field names it
  with `tstype:"<Name>"`, as it names the store/rbac/pkgmeta/extpipe enums
  wiregen reads. A limit a client checks is a constant in
  `internal/wire/limits.go`; a rule table the web needs (org role grants,
  terminal job statuses, inviting room roles) is computed by wiregen from
  its Go owner. The web re-exports these types (`export type { X } from
  "./wire.gen.ts"`) and never redeclares one (web boundary rule T8). A
  free-form payload (a reply's metrics map, a history row) is pinned by a
  golden fixture in `internal/wire/testdata/` that both test suites read
  (`readTurnMetrics` ≡ `TurnUsageFromMetrics`). The room stream listens for
  exactly the registry's kinds (`lib/roomEventKinds.ts` is the generated
  `RoomEventKindValues`). Component shapes are wire types too
  (`wire/components.go`, and the package parts in
  `wire/component_shapes.go`, which `core/pkgmeta` names for package
  authors by alias); `lib/apiTypes.ts` only re-exports them.

- **Requests, failures and org permissions have one owner each in the
  web.** `req` (`lib/api.ts`: `as: "json" | "none" | "text" | "blob" |
  "response"`, JSON only for a string body; `downloadCsv`) is the one
  authenticated request (boundary rules 1, 1b). A failed request is an
  `ApiError` (`lib/errors.ts`, rule 2), and a view renders any failure with
  `errorText(e, what)` and a status with `statusText` (rule 4; never
  `e instanceof Error ? e.message : …`). An organization permission is
  `can(role, perm)` (`lib/permissions.ts`, grants generated from
  `rbac.Can`; role literals only there and in `lib/roomRoles.ts`, rule 8):
  a viewer is offered no room creation and gets a read-only composer.

- **The other web owners** (each guarded by a rule of
  `web/scripts/check-boundaries.mjs` or a test; knip runs in
  `scripts/ci.sh web` and fails on an unused export):
  - an agent's handle and identity: `lib/roomAgents.ts` (`agentHandle`,
    `parseAgentHandle`, `agentIdentity`, `sameAgent`, `groupByCore`; exact
    owner/core/provider, rule 9); the composer's recipient precedence:
    `views/composer/recipient.ts`;
  - shared server state: `useQuery` keyed only by `queryKey(name, {subject,
    tenant})` / `publicQueryKey` (rule M10), cleared at sign-out and tenant
    switch; the shared lists in `lib/queries.ts` (`useMyAgents`,
    `useTenants`, `useQuotaRequests`, refreshed with `invalidateQueries`);
    the local-agent state is one module store (`lib/useLocalAgent.ts`);
  - polling: `useVisiblePoll` / `visiblePoll` only (rule 5);
  - browser storage: `lib/storageKeys.ts` (`StorageKey`, `memberKey`,
    `readStored`/`writeStored`, rule 3);
  - room API paths: `roomPath` (`lib/roomId.ts`, rule L2);
  - per-kind words and labels: `COMPONENT_KINDS` (`lib/componentKinds.ts`)
    and `CONTENT_KINDS` (`lib/contentComponents.ts`), `Record` tables over
    the kind unions (rule 12);
  - dates, money and bytes: `lib/format.ts` (rule 11); the room-gone
    wording: `roomGoneText` (`lib/roomGone.ts`);
  - list states: `ui/QueryState` (a load error is shown with Retry, never as
    an empty list); a destructive action: `ConfirmDialog` (rule 10b); a
    mutation's result is a toast, a load error stays inline;
  - a public link's app visitor token (handshake, per-share storage,
    renewal): `lib/appVisitor.ts` (rule A1; the share page's app view
    `views/share/SharedApp.tsx` only wires it); an app's owners' data line
    (counts, export, clear): `lib/appData.ts` + `views/workbench/AppDataLine.tsx`
    (shown for an `isApp` deliverable; `useAppStats` is its one query,
    whose answer also decides an app card's owner actions); an app's
    generated cover and when a cover is given up (`coverRefused`: 404,
    413, 415): `lib/appCover.ts` (a gradient seeded by the app id, the
    name's initial; `views/workbench/AppCover.tsx` draws a cover source,
    `siteCover` or `componentCover`, or it, and asks again for a refused
    one once `coverStored` says it exists); the deliverables' filters:
    `views/workbench/deliverableFilters.ts` ("Apps" is `isApp`); a
    member's works and apps (Library → Works: Published, Apps in rooms):
    `lib/myApps.ts` + `views/library/WorksTab.tsx`
    (who is an owner is the stats route's answer, never re-decided; an
    export is the server's audited route, the web only names the file);
    the works gallery (Plaza → Works): `lib/workGallery.ts` (the type
    filters and category tabs, the gallery filter, the author line, the
    recipe's chips and prompt preview, the publish form and request with
    the author's recipe control, the room choice and request of "Use this
    app" and "Make similar", the draft's hand-off) +
    `lib/workGalleryApi.ts` + `views/gallery/` (`PublishWorkDialog` is
    "Publish to gallery" on every deliverable card for the room's members
    who may post, `DeliverableOwner` from `views/workbench/deliverableOwner.ts`
    over the room's member list; `WorkPreview` renders a work with the
    artifact viewers; `AppDemo` frames a demo URL); what another view
    hands a room's composer (a draft's text, an expert, an agent,
    references, stored attachments): the `lib/team.ts` hand-offs
    (`composerDrafts`, `composerExpertPicks`, `composerAgentPicks`,
    `composerRefs`, `composerAttachments`) taken by
    `views/composer/useInjectedDraft.ts`, each once; a site frame's environment (desktop scheme, gateway base):
    `views/artifact/siteFrameEnv.ts`; what publishing to the organization
    says (by `publishApproval`) and its toast: `orgPublishCopy` /
    `orgPublishToast` (`lib/contentSharing.ts`);
  - where signing in brings a person back to (a share page of a link for
    the organization): `lib/loginReturn.ts` (rule A2; sessionStorage, a
    share hash only, taken once by `App`); a link's access choices and
    labels: `lib/shares.ts` (`DEFAULT_SHARE_ACCESS`, `shareAccessOptions`
    over the policy's `allowedShareAccess`, `shareAccessLabel`); what the
    share page does with a link (open, sign in, ticket, org-only):
    `openShareLink` in `lib/shares.ts`.

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

## Releases

A release that restarts a runtime reaches every turn in flight and every
workspace an agent keeps; a check of only the new path misses that
(`docs/prd/2026-10-13-live-turns.md`).

- **Run `scripts/release-check.sh` before and after** every service,
  desktop or core release: `bnbloc smoke`, then `bnbloc smoke --agent` for
  each agent given (or `BNBLOC_RELEASE_AGENTS`). A failure after the
  release that passed before it is the release's.
- **A core sidecar release first proves an existing workspace survives
  it.** On the operator's own desktop: one real turn in a room whose agent
  already has a workspace, the release, then another turn in that room —
  both must reply. Only then does it go out, and it goes out with
  `--rollout optin` first; `auto` follows once it has served real turns.
- **A core or desktop release runs the long-task scenario across the
  upgrade**: `bnbloc smoke --agent <agent> --scenario long-task --between
  "<the upgrade step>"` (or `BNBLOC_RELEASE_SCENARIO=1
  BNBLOC_RELEASE_BETWEEN=… scripts/release-check.sh`). It uploads a brief,
  has the agent write a workspace file, runs the upgrade step (which must
  return once the change is in effect), then asks for both: each of
  attachment, workspace write, memory and workspace file is its own
  PASS/FAIL line, and a failed run keeps its room for debugging.
- **A change to what an agent is told or given is proven on a resumed
  session of the real core, not on a new one.** Guidance, tools and
  instructions reach a session through a channel the core may freeze when it
  resumes a conversation (one core fixes its system prompt at the first turn
  and ignores a changed one), so a new session proves nothing about a kept
  one. Check the same session before and after: its files, its conversation,
  its tools and its instructions (`docs/architecture/core-providers.md`).
  A fix that only a new session shows is not a fix.
- **A release that touches rooms, records, attachments or borrowing runs
  the two-member check**: `scripts/two-member-check.sh <A's agent>
  <teammate provider>` (or `BNBLOC_RELEASE_TWO_MEMBERS=1
  BNBLOC_RELEASE_TEAMMATE_AGENT=… scripts/release-check.sh`). It makes a
  1-day API token for `svc:release-teammate`, runs a bridge of that
  teammate's on this machine (default core `acp` at `127.0.0.1:7341`), and
  runs `bnbloc smoke --scenario two-members`: the teammate's own agent must
  read A's message, A's agent's file and A's attachment, a borrow of A's
  agent must raise an access request A approves, and the borrowed turn must
  not see A's workspace. The bridge, token and temporary files are always
  removed.
- **Release notes are English** (`--notes-file`), one or two plain sentences
  on what changed for the member.
- **Never say a release recovers by itself** unless the code path that
  recovers was read and exercised.

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
  `BNBLOC_TEST_MYSQL_DSN` enables the MySQL integration tests (its user
  needs CREATE/DROP DATABASE: they run in throwaway databases).
- Web: `cd web && pnpm build` (`tsc -b && vite build`); unit tests:
  `node --test 'src/**/*.test.ts' 'scripts/*.test.mjs'` (Node 24); unused
  code: `pnpm --dir web exec knip`. After changing `internal/wire`, run
  `go generate ./internal/wire` and commit `web/src/lib/wire.gen.ts`.
- CLI (`cmd/bnbloc`): every gateway JSON call is `client.call` (returns
  every failure; a 401 carries the one sign-in hint, `responseError`), the
  gateway and signing flags come from `addGatewayFlags`/`addSigningFlags`,
  `whoami` reports `client.identity` (the session every command uses), and
  a package is held to `pkgmeta` before any upload
  (`TestOneCallHelperAndFlagSet`).
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
