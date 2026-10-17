# Room knowledge, memory and evolution as an installable component (2026-10-17)

> **WITHDRAWN 2026-10-17.** The platform code this PRD's stage 1 built (notes store, routes, `.room/memory/` files, the room panel, the `draft-note` assist) was the wrong shape and has been removed. The room's knowledge is to be built from the platform's existing Components (content components, schedules, deliverables), not compiled into the platform; the design is to be redone. Kept only as a record of what was tried. Left behind on purpose: the `room_notes` and `room_note_revisions` tables and migration `2026_10_room_notes` (already applied on QA; a later release drops them, `docs/design-review.md` and AGENTS.md "A migration only adds").
> Its extension framework (a new component kind with four contracts) was judged too heavy and is not to be built. 

Status: **redesign, not built; revised after a feasibility review (§8, 2026-10-17), owner confirmation of §8's direction pending. Where §8 and §2 differ, §8 wins.** Supersedes `2026-10-16-room-memory-and-evolution.md`,
whose stage 1 was built into the platform (notes store, routes, `.room/memory/` files,
a web panel): that was the wrong shape and is removed in stage 5 below. What that
PRD got right (the content model, the read tiers, the curator, the evolution path,
the review gates) moves into the component unchanged in intent.

## 0. Why the first design was wrong

The product owner asked for this to be an installable component. The first design
made it platform code: note types, ranking, file wording and a panel compiled into
the gateway, the room service, the bridge and the web app. Only the AI drafting
prompt was content. That breaks the standing rules (platform = the minimal base,
`platform-minimal-base`; features as components with contracts, the way
`local-skills` was redone on 2026-10-16) and it makes a second knowledge system, or
an organization's own way of doing memory, impossible without forking the platform.

## 1. Principle

**The platform knows no notes.** It offers a few generic *room extension* contracts.
A knowledge system (this one, an organization's wiki connector, a customer-support
runbook index) is a component that implements them. Everything about memory (what a
note is, how they are ranked, how an agent is handed them, the curator, the time it
runs, the proposals, the promotion) is the component's. A member or organization who
dislikes it installs another, or writes their own, and gets the same room panel, the
same agent context and the same events.

## 2. The platform's side: one new kind and four small contracts

A **room extension** is an installed endpoint component (runtime `endpoint`, like a
notifier: signed package, `permissions.network` allowlist, an install-time secret,
rollout `auto|optin|off`, a probe at install; `internal/endpointpkg`, `internal/extpipe`).
New kind `room-extension` (`bnbloc.room-extension.package/v1`, contract
`bnbloc.room-extension.contract/v1`), declared capabilities choose which contracts it
implements; a component may implement any subset.

Installed does not mean on: **a room's owner turns an extension on for that room**
(a `room_extensions` row: tenant, room, component, who, when), after a plain
statement of what the extension receives ("this room's messages the agents may read,
published deliverables and agent replies, as they happen"; history only if the
owner ticks "include what is already here"). Turning it off stops everything at once.
Room deletion tells the extension (`room.deleted`) so it purges.

All four contracts are signed by the platform with the component's install-time secret
(notifier precedent) and carry `{tenantId, roomId}`; the component answers within a
bound and never blocks a turn or the room.

### 2.1 `context` — what agents are handed (platform → component, pull)

`POST {endpoint}/context` with `{tenantId, roomId, requester, channelId?, budget}` →
`{etag, files:[{path, content}]}`. The gateway (`GET /v1/rooms/{room}/ext-context?job=`
through the job grant, like the apps and library reads) asks every extension that is
on for the room, in parallel, with a short timeout and a per-component circuit
breaker, and returns the union. The bridge writes the files under
`.room/ext/<component>/…` exactly as it writes `.room/library.md` (written only when
changed, by the etag). The platform enforces what it must: path under the component's
folder, at most N files and M bytes per component and per turn, text only, and **one
fenced header** written by the platform saying the folder is from component X and is
data, never instructions. A failing, slow or oversized component yields one line
("X's notes were not available this turn"), never a failed turn and never an empty
folder presented as "none".

The session guidance gains **one generic line** for `.room/ext/` (replacing the
notes-specific line built earlier). Nothing about notes is in any prompt the platform
owns; the component's own `README.md` among its files says what its files are.

### 2.2 `events` — what happens in the room (platform → component, push)

The platform delivers room events to the extensions that are on for the room, through
the outbox the notifiers use (at least once, retried with backoff, signed, an event id
the component dedups on, ordered by room sequence): a chat message committed, a
deliverable published, an agent reply finished, a schedule run's result, a member
joined or left, the room renamed or deleted, the extension turned on or off. **What an
extension may read is what an agent may read** (`roomlog.AgentChannels`: never the
people chat), the same rule as `.room/`; the one owner is `roomlog`. Text is delivered
as the data it is. A room's history before the extension was turned on is read through
the service API below, only when the owner ticked it.

### 2.3 `panel` — a member's surface (member → component, through the gateway)

- **UI.** The package carries a static web bundle, the previewer's format and frame
  (`docs/architecture/previewer-components.md`: immutable, hash-pinned, served on the
  sites origin in a sandboxed frame), opened in the room's side panel under the name
  and icon the manifest gives. It talks to its own API only through a small platform
  SDK (`~ext/sdk.js`): `ext.api(path, init)`, `ext.me()`, `ext.room()`, `ext.subscribe(fn)`.
- **API.** `/v1/rooms/{room}/ext/{component}/api/{path...}` is a gateway route that
  admits a member of the room, checks the extension is on for it and forwards to the
  component signed with `{subject, tenant, room, role}`; the component enforces its own
  rules (who may edit, who may review) from that identity. Bounded bodies, rate limits
  per component, an audit row per mutating call (method and path, never the body).
- **Contributed actions.** A manifest may declare `messageActions` (for example "Save to
  memory"): the web shows them on a message's actions and opens the panel with the
  message reference. No component name appears in web code.

### 2.4 `service API` — what a component may ask of the platform (component → platform)

Service-signed (the install secret), scoped to the rooms where it is on, audited,
rate-limited, per tenant:

- `GET /v1/ext/rooms/{room}/history` — the agent-readable history after a cursor
  (the same `roomlog.History`), and only for an extension that is on; it is how a
  component backfills and how a curator catches up after downtime.
- `POST /v1/ext/assist/{task}` — run an assist task (`docs/architecture/ai-assist.md`)
  as the component, on the tenant's model and budget, audited as `assist` with the
  component named. The component's prompts are content (assist-kind components it
  `requires`, publishable and overridable by the organization like any assist), not
  strings in its code. It never gets a model key.
- Later, only when a stage needs it: `POST /v1/ext/library/proposals` (hand a draft
  skill or expert to a person to approve through the Library's own request-and-approve
  flow, never publish) and `POST /v1/ext/rooms/{room}/notices` (send a notice through
  the one notify path as the room's extension).

That is the whole platform surface. It is generic: nothing in it names notes, a
brief, a lesson or a curator.

## 3. The component: what it owns (`room-memory`)

Public reference implementation: `cmd/bnbloc-room-memory`, package
`packages/room-memory`, its own storage (MySQL schema of its own, or an embedded
store for a small deployment), run beside the gateway like the notifier services.
It owns all of the first PRD's domain, now in its own code and configuration:

- **Content model.** Types (brief, knowledge, memory, lesson), status, supersession,
  revisions, sources, verification dates, caps and the ranking rule are the
  component's, with its policy as a data file it ships (the `policy.default.json`
  precedent from local-skills: the order of types, the caps, the decay), which an
  organization overrides in the component's own settings. No type, limit or weight
  is in a platform Go file.
- **What agents read.** It answers `context` with `BRIEF.md`, `INDEX.md`, `notes.md`
  and a `README.md`, ranked and capped by its own policy; the three-tier reading, the
  fencing of others' text inside them and the honest "could not be read" wording are
  its own (the platform's header is a second, outer fence).
- **Writing.** People write through its panel; its AI drafting and tightening
  ("Draft with AI", "Improve with AI", a brief from the room) call the platform's
  assist with the `draft-note` task, which moves out of `internal/contentbuiltin` into
  the component's package as a content dependency.
- **The curator and its schedule.** It reads events, keeps a cursor per room and runs
  its own nightly pass **per room, at the time and in the time zone that room's owner
  sets in the component's panel** (its settings, never a deployment constant and never
  the platform's scheduler). Evidence thresholds, daily proposal caps, auto-apply for
  low-risk appends, expiry after 14 days and the review inbox are its settings. It
  asks the platform's assist for the model work and spends the tenant's budget,
  rate-limited by the platform.
- **Evolution.** Lessons, proposals against a component version and promotion to the
  organization are the component's flows. Anything that leaves the room goes through
  the room owner's side-by-side gate in its panel, a secret scan, and then the
  platform's `library/proposals`, where the organization admin approves. The component
  has no power to publish.
- **Cross-room discovery** stays out for now; a tenant-level curator would be a second
  component (or a setting of this one) with an organization-admin opt-in, built on the
  same contracts.
- **Other knowledge systems fit the same four contracts**: a wiki connector is a
  component that answers `context` from the wiki and has no `events` at all.

## 4. Where each earlier decision went

| Earlier decision | Now |
|---|---|
| A room is a project and memory belongs to it | `room_extensions` is per room; the component keys everything by room |
| Humans review, the curator proposes | the component's inbox; the platform has no inbox |
| Curation off until the owner turns it on, with their own time | the platform's per-room enablement plus the component's own per-room schedule |
| One store, three kinds of content | the component's schema; the platform stores none of it |
| No agent-facing search; files, not tools | `context` files only; no tool is added |
| Promotion to the organization, nominations by AI, people review | the component drafts, the owner gates, `library/proposals` and the Library's approval gate |
| Cross-room discovery as a later opt-in | the component's later setting, same contracts |
| AI in every feature | the component's AI calls go through the platform's assist; lens 13 below |
| Retrieval behind one interface, swappable | internal to the component |

## 5. Design review

| Lens | Answer |
|---|---|
| Actors | Super admin installs; org admin controls rollout; room owner turns an extension on for a room and sets what it may read; members use the panel under the identity the gateway signs; the component enforces its own roles from that identity; agents only read files under `.room/ext/`; a borrowed run's agent reads the files the requester's membership allows (the job grant), never more; the component never acts as a member. |
| Time and lifecycle | Events are at-least-once and ordered per room with ids to dedup; a component down for a day catches up from its cursor through the history API; turning off stops delivery at once and the component keeps (or purges, its policy) what it has, with `room.deleted` obliging it to purge; uninstall removes the room rows and tells the owner; a long curator pass is the component's own concern, with no platform clock. |
| Scale and cost | Context is pulled per turn with an etag, a timeout and a breaker, so a slow component costs one line, not latency; per-component file and byte caps; events are batched; the component's model use is the tenant's assist budget and rate; many rooms choosing "02:00" is the component's staggering to do and the platform's rate limit to enforce. |
| Failure and recovery | A failing component never fails a turn or a message (timeouts, breaker, "not available this turn" line); events retry with backoff and surface in the audit; the panel shows the component's error text verbatim, not a guess; a component that answers garbage is dropped for the turn and logged. |
| Boundaries and privacy | The biggest one: **room content leaves the platform for the component's host.** So it is opt-in per room by the owner with a statement of exactly what is sent, limited to what an agent may read, never the people chat, signed and bounded, recorded in the audit (who turned it on and when), and revocable at once. The component's host is the organization's own (network allowlist); the platform never sends content to an extension that is off for the room. Files an extension writes are confined to its folder and fenced as data. |
| Abuse and untrusted input | The component is semi-trusted code; its output is untrusted text (fence, caps, text only, no path outside its folder); it cannot publish to the Library, change permissions, or call a tool; its service calls are scoped to the rooms where it is on and rate-limited; a malicious or compromised component can read what it was granted, which is why granting is explicit and per room. Room text reaching a prompt is the component's to guard, with the assist's own data fencing as the floor. |
| One owner | The extension framework (kind, install, enablement, signing, fan-out, proxy) is one package; `roomlog` owns what an extension may read; the Library keeps owning organization visibility and approval; `assist` keeps owning every model call; the notes domain has one owner, the component. An architecture test forbids a component name or a notes concept in platform code. |
| Defaults and fallbacks | Nothing is on by default; no extension installed means no panel entry, no context, no events; the feature is simply absent. A component without the `events` capability still works as a context source. |
| External contracts | New: kind and contract, four contracts versioned `…/v1`, the room enablement route, `.room/ext/` layout, the `~ext` SDK. Additive; nothing removed except the stage 1 code (pre-launch, no compatibility). |
| Load on users and agents | Agents: one generic guidance line and files they already read, no tool. People: the panel is the component's; the platform adds one enablement toggle with an honest statement. |
| Release and compatibility | The component versions and ships independently of the platform; contracts are versioned, additive within `v1`; a contract change is `v2` beside `v1`; the platform tolerates an older component and a component tolerates a platform without an optional contract. |
| Observability | Per-extension counters (context latency, timeouts, breaker state, files and bytes, events delivered, failed, retried, service calls), the audit rows above, and the panel showing each component's last error. |
| AI capability | The component is AI-first by design (the curator, drafting, tightening, generalization drafts), all through the platform's assist as content; a person decides everything that changes shared state; with the model off the component still stores and serves notes. The platform seams themselves need no AI. |
| Component or platform | Every capability above was tested against "can a component do this on its own?" The platform keeps only what a component cannot do safely or at all: enablement and consent, signing and confinement, fan-out of events it alone sees, the member-authenticated proxy, the history rule, the budget and the audit. |

**Pre-mortem.** A year on, extensions are unpopular or dangerous. Likely reasons and the
guards: a slow component makes every turn slow (timeouts, breaker, etag cache,
"not available" line); a component leaks a room's content (opt-in per room with an
explicit statement, agent-readable scope only, network allowlist, audit, revoke at
once); nobody can write a second component because the contracts are vague (they are
specified with schemas and a conformance test kit, a fake extension in this repo's
tests is the reference); the platform quietly grows note-specific code again (the
architecture test and lens 14); versioning breaks old components (additive `v1`,
tolerant both ways).

## 6. Stages

1. **The framework.** Kind and package validation, install and probe through the
   shared endpoint path, per-room enablement with its consent text and audit,
   `context` fan-out with the gateway read and the bridge write, `events` through the
   outbox, `panel` (frame, SDK, gateway proxy, message actions), the service API
   (`history`, `assist`), the generic guidance line, architecture tests, and a
   **conformance test kit with a fake extension**. No memory in it.
2. **The `room-memory` component.** Its store and policy, `context` files, `events`
   intake, the panel UI (rewritten as the component's bundle; the stage 1 React panel
   is its starting material), AI drafting, the per-room curator schedule and inbox.
3. **Lessons, proposals and promotion** (needs `library/proposals`).
4. **A second implementation to prove the contracts** (a read-only wiki connector
   answering `context`), because contracts designed for one user are not yet contracts.
5. **Remove the stage 1 platform code**, in one change with stage 2's first release so
   nothing is left half there: `internal/notes`, `internal/store/room_notes.go` and its
   tables (through the retired-table mechanism after checking what was deployed),
   `services/room/room_notes.go`, the gateway `/memory` route, `roomlog/memory.go`
   and the notes guidance line, the web panel and "Save to memory", the wire types,
   the architecture test and the AGENTS.md paragraph; `draft-note` moves into the
   component.

## 7. Open items

- **Where the component's storage lives** in a small deployment (an embedded database
  under the component's own data directory is the default I would take, MySQL for a
  production one).
- **Consent wording** for turning an extension on, to be agreed with the owner; it must
  say what is sent and where.
- **Frame origin and the SDK's surface** (is `ext.subscribe` needed in v1, how the
  panel is opened on desktop and web alike): to verify against the previewer frame and
  the desktop's site scheme at build.
- Whether the history rule should let a room owner opt an extension into the people chat
  (this design says never).
- Verification gaps from the first PRD that still apply: whether the OpenSearch cluster
  has vector support and the embedding setting (now the component's business).

## 8. Feasibility review (2026-10-17) and the revised design

Four read-only reviews checked §2 against the code (extension framework and outbox;
the per-turn context path; the panel, frame and proxy; events, history and assist).
Evidence is `file:line` in each finding; "unverified" means not confirmable from this
repo.

### 8.1 Findings that change the design

1. **`events` push cannot be built on the notification outbox.** The outbox is unordered
   (a retried row is overtaken; no room sequence in the payload), lossy (the observer's
   512-slot queue drops, retries stop after 8 attempts or about 21 minutes, failed rows
   are pruned after 7 days), cross-tenant and not room-owned, with one sequential worker
   and no breaker (`internal/notify/worker.go`, `observer.go`,
   `internal/store/mysql_notifications.go`). There is no durable ordered event source
   anywhere: the commit hook (`Hub.Committed`) is in-memory, agent replies reach realtime
   by a best-effort HTTP call, and rename, schedule result and "member left" have no
   event. A real push feed means a per-room event log written with `room_messages`.
2. **The panel cannot be a previewer-style frame.** The previewer frame is on the main
   origin with `connect-src 'none'` in Go and Rust, a package shape that forbids network
   and API, and a file-oriented message set (`services/gateway/previewers.go`,
   `previewer_scheme.rs`, `previewer_validation.go`); a gateway-origin frame is blocked
   by the desktop CSP, and the site scheme forwards no authorization, caps bodies at 68
   KiB and allows non-GET only under `~app/`. §2.3's "sites origin" was wrong.
3. **The gateway proxy must not forward a member to a third party as it is.** `proxyAs`
   does not strip `Authorization`, and the internal signature covers only
   `ts|user|tenant|role` for 5 minutes, not method, path or body
   (`service.go:737-742`, `svcauth.go:91-134`); the notifier signature covers timestamp
   and body only (`notify/sign.go:27`). A new scheme and a header allowlist are needed.
4. **Callback identity has no precedent for a component naming its own room.** Existing
   callbacks derive tenant and room from a capability the platform minted (reply token,
   connect code); the install secret is one per component name platform-wide, with no
   kind or tenant in its key (`notify/secrets.go:17`), so a name could collide with a
   notifier and one leak reaches every enabled room.
5. **A component cannot read history as a service today.** A `svc:` subject has no room
   membership; adding it as a member makes it `IsHuman()` and skips the people-chat
   guard; `/history` is backward and per channel; the forward `/feed` is internal and
   unguarded. A read-only "extension reader" identity and a forward, agent-scoped,
   cursor feed with `retainedFrom` and revisions are needed. Edit, delete and retention
   have no event (hard delete, only `retainedFrom`; the revisions cursor can skip rows
   sharing a millisecond).
6. **Assist has no non-member caller**, the 20/min bucket is per member, there is no
   tenant spend cap, and audit names a person (`assist.go`, `routes.go:388-394`).
7. **Context fan-out is feasible but needs more than §2.1 said.** The pre-turn pass is
   sequential with a 20 s per-request bound and no overall budget, and it runs inside
   the turn's deadline and start gate (`roomctx.go:155-206`, `bridge.go:449,1439`); a
   write failure drops the whole pass to the push prompt (`roomctx.go:297-301`); `.room/`
   files are never deleted, so an off or removed extension leaves stale files; one
   header is a weaker fence than the per-body content-tagged fences used today
   (`markers.go:36-46`); channel sessions of one seat share a workspace and run
   concurrently, so a per-channel `channelId` input would let one turn overwrite what
   another is reading (`roomctx.go:215-217`); a core without the file writer gets no
   `.room/` at all (only `manifestcore` and `protocol` implement it in this repo; whether
   the cores installed outside this repo do is **unverified**); the
   guidance budget has no headroom (a generic line costs about 29 tokens).
8. **Size.** The kind scaffolding alone is about 10 files and 400 to 600 lines
   (notifier-hardcoded pieces: secret store, install hooks, visibility, CLI, web); the
   four contracts, `room_extensions`, the proxy, the feed, the service identity and the
   web registries bring the total to an estimated 40 to 60 files and 5,000 to 8,000
   lines, before any memory behavior. An estimate, not a measurement.
9. **Two things the reviews confirm as sound:** the job-grant pattern for per-turn reads
   (`content_requester.go`) and borrowed runs (separate seat workspace per borrower, no
   cross-leak path found); and `roomlog.AgentChannels` as the one rule of what an
   agent-side reader may see.

### 8.2 The revised design: the same principle, a smaller platform

The principle stands: the platform knows no notes. What changes is how little it
adds. v1 of the room-extension framework is **three contracts, all pull or request/response**:

1. **`context` (platform → component, pull)** as §2.1, with these changes: the bridge
   fetches it in a **separate, best-effort step** after the existing pass (a failure
   never falls back to the push prompt), **in parallel across components with one hard
   overall budget well under the turn's**, with the last good answer cached by etag;
   the request has **no `channelId`** (one workspace, many channels); files are written
   under `.room/ext/<component>/` with a per-component manifest and **tombstone**
   semantics (a removed or off component's files are overwritten with a one-line note);
   the platform fences **each file's content** with the existing content-tagged fence,
   not one header; a hard count and byte cap lives in the bridge (cores enforce none);
   a core without the file writer gets nothing, said plainly in the docs (verify per
   core). The component-down case is a per-component circuit breaker, which is new.
2. **`panel` as a host-rendered declarative UI, not a frame.** The component answers
   typed JSON (a list, a form, a detail, actions, a diff, a message reference) that the
   web renders in host-owned React, the way Settings → Skills renders a provider and
   plugin settings render forms. No frame, no CSP change, no desktop scheme work, no
   SDK, no tickets. Calls are `POST /v1/rooms/{room}/ext/{component}/call {op, args}`
   through the gateway: a **new signature** over method, path, body hash, room, subject,
   role, nonce and a short expiry, a **header allowlist** (never the member's bearer),
   body caps, a rate limit per component and per subject, an audit kind, and a
   streaming-capable client. The cost is expressiveness (a fixed set of widgets, a
   component cannot draw its own UI), which a notes list, an editor and a review inbox do
   not need; richer UIs are a later contract (`panel/v2`, a frame via host-relayed
   `postMessage`, the reviewed option) if a component needs one. `messageActions` and
   the panel entry come from a **code-owned registry** in the web (icons from a fixed list).
3. **`service` (component → platform), pull only:** a **forward, agent-scoped feed** of a
   room (`GET /v1/ext/rooms/{room}/feed?after=`, cursor, `retainedFrom`, edits and
   deletes as tombstone entries, applying `roomlog.AgentChannels` itself) for rooms
   where the component is on; the component derives tenant and room from the grant
   (**not** from its request body), with a **per-kind, per-name secret** and the new
   signature scheme; and assist runs as the component with its own limiter, an audit
   actor naming it and a per-tenant cap (new). There is **no `events` push in v1**: the
   component polls the feed on its own clock, which also gives it the cursor and replay
   that push could not. Push can arrive later as `events/v1` on a per-room durable log.

Everything else in §2 stands: the kind, install through the shared endpoint path,
per-room enablement with the consent text and audit, tombstoned `room.deleted` (an
outbox row that outlives the room, or an enqueue inside the delete transaction),
conformance kit with a fake extension, no note vocabulary in platform code.

### 8.3 Residual risks the revision does not remove

- **Operations.** An organization runs one more service and its database (the
  notifier precedent, but heavier: it stores room knowledge). A small embedded store
  option helps; the burden is real.
- **Room content leaves the platform for the component's host.** Opt-in per room,
  agent-readable scope only, network allowlist, audit and revoke are the guards; they
  are guards, not a proof.
- **Cores.** Delivery of the files depends on each core's file writer (unverified for
  the ones outside this repo).
- **A deleted or retained-away message** leaves the notes distilled from it; the
  component must honor the feed's tombstones, and distillation is not exactly
  traceable. Said to the room owner at enable time.
- **Order of work.** The framework precedes any memory behavior, so the first release
  that a member sees is the whole stage 5 swap; until then the stage 1 platform code
  stays.

### 8.4 The alternative that was considered: an app plus one seam

An interactive app already has storage, privacy, a UI, `app.ai`, per-room schedules with
their own time zone, and a Library path, and would need only a generic "files an app
offers its room's agents" seam and a history-read and write action for schedule runs.
Rejected as the primary design: an app is built by an agent from a prompt, not
installed as a deterministic package (a work saves its recipe, not its bundle), an app
has no server-side compute (ranking and caps would live in page code that runs only
when someone opens it), and a Library app is not what the owner meant by an installable
component. It remains the right shape for room-local tools that need none of the above.
