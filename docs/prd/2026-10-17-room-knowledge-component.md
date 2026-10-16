# Room knowledge, memory and evolution as an installable component (2026-10-17)

Status: **redesign, not built.** Supersedes `2026-10-16-room-memory-and-evolution.md`,
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
