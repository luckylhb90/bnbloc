# Room tools: agents dispatch agents

Design: `docs/prd/2026-10-12-experts-and-teams.md` §7 (delivery step 2).

An agent the bridge drives can see the room's agents and ask one of them
for part of its work. The dispatched task is an ordinary room message and
an ordinary agent job, posted **on behalf of the person who asked the
dispatching agent**. Nothing about authorization is new: the target bridge
sees a job whose requester is that person and runs borrow authorization,
reservation, quota, budgets, sandbox policy and the platform default model
exactly as for a message the person typed.

```
agent CLI ──MCP──▶ bridge room tools server (127.0.0.1, per-session bearer)
                     │ bridge's own session
                     ▼
gateway POST /v1/rooms/{room}/dispatch      (room scope, drive_agent, chat rate limit)
                     │ rewritten + svcauth-signed as the bridge owner
                     ▼
realtime POST /internal/v1/rooms/{room}/dispatch   (RouteKey → the room's owner replica)
                     │ guards, then PostChat as the parent job's requester
                     ▼
chat_message {dispatchedBy} + agent job {dispatchedByJobId, dispatchDepth}
                     │ claimed by the target agent's bridge like any job
                     ▼
reply in the room; deliverables published with sourceSeq = the dispatch's seq
```

## The bridge MCP server (`services/bridge/roomtools`)

- **One server per bridge**, loopback (`127.0.0.1`, random port), endpoint
  `/mcp`. It refuses a non-loopback `Origin` or `Host` (DNS rebinding) and
  answers `GET`/`DELETE` with 405.
- **One bearer per driven core session.** The bridge's session contributor
  hook (`services/bridge/profile.go`) asks for the session's contribution
  when the session is created on an `agentProfile` core: the server mints a
  random bearer and the session's profile gets the server, either
  - `http` with an `Authorization: Bearer …` header, when the target
    advertises http MCP (`core.Target.MCPTransports`), or
  - the stdio shim `bnbloc-bridge roomtools-stdio --url <endpoint>` with the
    bearer in `BNBLOC_ROOMTOOLS_TOKEN` (env, never argv). ACP requires every
    agent to accept stdio servers.
  Bearers are looked up by hash and compared in constant time; the release
  hook revokes a bearer when the bridge drops the session binding (rebind,
  forgotten room, core endpoint change), and a bridge stop revokes them all.
- **Bound to the current job.** The turn hook binds the bearer to the job
  each turn drives. A bearer only ever dispatches on behalf of that job, and
  realtime admits the dispatch only while the job is running on a bridge of
  the caller, so a bearer that outlives its turn dispatches nothing.
- **The agent never sees the bridge's session.** Every gateway call is made
  by the bridge's own authenticated client; the bearer is meaningless
  outside the bridge process.
- **Protocol:** MCP Streamable HTTP, dual-era. Legacy clients
  (2025-03-26 … 2025-11-25) open with `initialize`; modern clients
  (2026-07-28) carry `_meta.io.modelcontextprotocol/protocolVersion` per
  request and mirror it into `MCP-Protocol-Version` / `Mcp-Method` /
  `Mcp-Name` (checked: `-32020 HeaderMismatch`, `-32022` unsupported
  version, 404 for an unknown method, `server/discover`). No protocol
  sessions are minted. A tool call carrying a progress token from a client
  that accepts SSE is answered as a stream of `notifications/progress`
  followed by the result (ask_agent's wait). The stdio shim is a stateless
  relay: one POST per stdin line, every JSON-RPC message of the answer as
  one stdout line, headers mirrored from the body, `notifications/cancelled`
  aborting the matching request.
- **Tools** (one registry: dispatch, step 2; content authoring, step 5):

| Tool | Reads / writes |
| --- | --- |
| `list_room_agents()` | `GET /v1/rooms/{room}/agents` (the live roster); handles `owner/provider`, or `owner/core/provider` where one owner exposes a provider on two cores. |
| `ask_agent(handle, task, expert?, wait_seconds?)` | A team lead may pass a role of its roster as the handle (below). `POST /v1/rooms/{room}/dispatch` with the bound job as parent, then polls `GET /v1/rooms/{room}/jobs/{jobId}` (default 10 min, at most 20, below the bridge's 30-minute turn cap). Settled: the reply text (the run's session replies in the channel history, up to the run's finish) and the deliverables `GET …/artifacts?sourceSeq=<dispatch seq>` as `[{name, kind, link}]`. Still running: the handle. |
| `room_status(handle?)` | The run record of one task this session dispatched (with reply and deliverables once settled), or all of them. |
| `read_room(since?, limit?)` | History of every channel an agent may read (`roomlog.History`: the room's channel listing, never the people chat), people's messages and agents' replies, notices skipped. |
| `save_component(dir, id?)` | Reads every file under `dir` (workspace-relative, e.g. `drafts/reviewer`) through the core's session-files seam (`core.SessionFiles`, handed to the tools per turn as `TurnInfo.Workspace`), packs them (`tarbundle`, content limits) and posts the archive to `POST /v1/content-components` (or `…/{id}/versions`) with the bound job's `?job=&room=` grant: the gateway validates it and saves it as the job's requester. The validator's message comes back as the tool error. A core without session files cannot save; the agent points the member at "Save as expert" under its reply's files. When the organization uses the component, the result carries `resubmitted` and the note tells the agent to say the new version waits for an org admin. Every granted save or publish puts a transient `content_saved` event in the room, which the member's workshop toasts. |
| `get_component(id, version?)` | `GET /v1/content-components/{id}[/versions/{v}]` with the grant: the manifest and every text file, as the requester may read it (the "Improve" flow). |
| `list_components(kind?)` | `GET /v1/components` with the grant (`roomlog.ReadLibrary`): the requester's whole Library, most recently updated first: experts, skill packs, prompt packs (`kind` `expert`, `skill`, `prompt`) and MCP tools (`mcp`), with version, scope, `mine` and the update time. |
| `read_artifact(artifactId, path?, version?)` | A deliverable of the session's room (below), through the room's own artifact routes. |
| `publish_component(id, scope, note?)` | Posts to `POST /v1/content-components/{id}/publish` with the job's grant, as the requester: `org` opens a publish request an organization admin approves (the reply carries the request and the version members keep meanwhile), `personal` withdraws it and takes the component out of the organization; `platform` is answered locally as a platform admin's action ([content-components.md](content-components.md), "Sharing and publishing"). |
| `post_progress(text)` | Posts one progress line of the bound job as a room notice (below): at most 20 per job, 500 characters each. |
| `try_draft(dir, task, handle?, wait_seconds?)` | A real quick try of an unsaved expert or skill pack (below): packs `dir` like `save_component`, validates it with `contentpkg` (the error goes back to the agent), stages it with the job grant (`POST /v1/content-drafts`) and dispatches `task` to another agent wearing it through the same door, guards and wait as `ask_agent`. |

**Content tools' authority** is the dispatch model applied to the content
routes (`docs/architecture/content-components.md`, "Requests on behalf of a
job's requester"): the grant is admitted only while the bound job runs on a
bridge of the caller and its requester is a human member of the room; the
save lands in the requester's Library; a borrowed or platform agent saves
no script-bearing package.

Room tools need the bridge and the agent CLI on the same host (the desktop
sidecar case). A sandboxed drive still works: the agent CLI and its MCP
servers stay on the host; only shell commands run in the microVM.

## What every agent is told (with or without room tools)

An agent that did not know each agent has its own workspace looked for
another agent's drafts in its own, found none, and called them never
written (`docs/bugs/2026-10-12-agent-misreads-other-agents-work.md`). The
room model is therefore stated to every agent, on every core, whether its
targets advertise `mcpTransports` (and get these tools) or not:

- **The room model, once per session** (`roomlog.SessionGuidance`):
  every agent works in its own workspace; deliverables are stored by
  bnbloc and named by their producer, never files of the reader's
  workspace; the Library is bnbloc's Library of the person who asked
  (experts, skill packs, prompt packs, MCP tools), shared by every agent and
  core in the room; how to verify, with these tools or without them. It is
  static guidance, so it never rides a turn's prompt: the bridge's
  `sessionGuidance` (`services/bridge/profile.go`, the one owner of where
  session guidance goes) puts it at the head of the agent profile the
  session is created with on a core advertising `agentProfile` (a stable
  prefix providers cache; outside the binding sum, like the session
  contributors' parts), else at the head of the session's first prompt,
  through the expert fallback's prefix. `.room/README.md`
  (`roomlog.ReadmeFile`) keeps the same text in a pull workspace, written
  only when it differs from what the session's workspace holds
  (`roomlog.ReadmeUpdate`, `CtxState.ReadmeSum`).
- **`.room/artifacts.md`** (`roomlog.ReadDeliverables`,
  `DeliverablesFile`): the room's `roomlog.MaxListedDeliverables` newest
  deliverables, each with kind, latest version, id and producer, marked
  "NOT in this workspace" (the reader's own: "from you"). The producer of a
  version is `roomlog.VersionProducer`: its source job's agent from the run
  record (`owner/provider` and core), else its author (a person's upload).
  The bridge caches resolved versions (`roomlog.ProducerCache`; a version
  never changes), so a turn costs one listing.
- **`.room/library.md`** (`roomlog.ReadLibrary`, `LibraryFile`): the
  requester's Library read with the turn's job grant, the
  `roomlog.MaxLibraryItems` most recently updated (name, kind, version,
  scope, updated time). The gateway's visibility owners decide what is in
  it (content-components.md, "The Library as an agent reads it").

The bridge rewrites `artifacts.md` and `library.md` every turn
(`roomRecordFiles`, beside the history and `live.md`: their data changes);
a read that fails renders as unreadable, never as empty, and never fails the
turn. The per-turn pull prompt (`roomlog.ComposePointer`) is a plain list of
these files with no guidance in it, pinned by its golden
(`testdata/golden/pointer.txt`). `.room/` is a bnbloc-owned path
(`core.BnblocOwnedPath`): never a deliverable, synced or packed.

**`read_artifact`** (`services/bridge/roomtools/artifact.go`) lets an agent
with tools check another agent's work: the artifact detail and version
routes (`GET /v1/rooms/{room}/artifacts/{id}[/versions/{v}]`) as the bridge's
session, so the room service refuses a non-member and an artifact of
another room (the tool also refuses a detail naming another room); a blob
version through its content route, a site's file (`path`, default
`index.html`) through a site ticket and the gateway's site file rules. A
stored version over 4 MiB is not downloaded; the text returned is capped at
64 KiB (`truncated`), binary content is not shown, and the text is fenced
between markers carrying a random nonce, naming its producer, as data to
check, never instructions.

## Teams

A turn wearing a team expert ([`content-components.md`](content-components.md),
"Expert teams") binds its roster to the session (`roomtools.Job.Team`:
each role's agent or why none, and its resolved expert or why none). The
guards hold in the tool layer (`services/bridge/roomtools/team.go`):

- **Roles.** `ask_agent(handle: "<role>")` dispatches to the role's agent
  wearing the role's expert (`expert` = its `id@version`) with
  `role` on the dispatch. A handle without "/" that is no role is refused
  as not covered by the roster ("refuse that part instead of improvising a
  role"); an unstaffed role or one whose expert does not resolve names
  why; a role refuses an explicit `expert`. A lead may still name an agent
  handle.
- **Failures.** A member task that ends `failed`, `timed_out` or
  `cancelled`, or a dispatch refused, counts against the request; after
  `wire.MaxTeamFailures` (3, a protocol safety constant beside the depth
  and fan-out limits) `ask_agent` refuses further dispatches and tells the
  lead to stop and report.
- **Timeouts.** A member task still running when `ask_agent` stops waiting
  posts a notice ("critic (maya/opencode-b) has not finished after 10
  minutes; …"); the lead checks it later with `room_status`.
- **Notices.** `post_progress` lines and timeout notices are
  `agent_messages` of the job's own activity session
  `job-<jobId>-progress` (under the job notice namespace, so they never
  move a reply cursor, never count as the agent's reply and never trigger
  a reply notification), with wall-clock sequences and `payload.notice`
  (`progress`, `member-timeout`); the feed renders them as one compact
  line and they leave the agent's working indicator alone.

The per-job state (failures, progress lines) resets when the session binds
another job.

## Quick tries of drafts (`try_draft`)

The Expert builder tries what it drafted before the member saves it
(PRD "Runtime gap closure" 4). An unsaved draft cannot be a content
component (that would list it in a Library), so it is staged as an
**ephemeral draft** instead:

- **Staging.** `POST /v1/content-drafts?job=&room=` (gateway, the content
  grant of [content-components.md](content-components.md), "Requests on
  behalf of a job's requester") validates the archive like a save (an
  expert or a skill pack; a team is refused), stores the bundle in the
  blob store and a `content_drafts` row: the staging job, its room, the
  requester, the author (the staging agent's owner) and an expiry
  (`store.ContentDraftTTL`, one hour). It answers `wire.ContentDraft
  {id, kind, name, displayName?, version, expiresAtMs}`.
- **Dispatch.** `wire.DispatchRequest.draft` names it (never with
  `expert`). Realtime admits only a draft the parent job staged, in this
  room and tenant, before it expires (400 `draft_not_available`
  otherwise), after every guard below; the job records it as its expert
  (`expert_id` = the draft id, name "X (draft)"), the message's
  `payload.expert` carries `draft: true` and the audit row is a content
  `try`.
- **Wearing.** `contentuse.Effective` resolves a draft expert id only for
  that dispatched job (`DispatchedByJobID` = the staging job), before
  expiry, with the draft's author as trust owner (someone else's
  script-bearing draft forces the sandbox); the bundle is granted through
  the job grant only. An expired draft is reported missing and the turn is
  refused like an unavailable expert.
- **Target.** `handle`, else the requester's own other agent, else another
  online agent of the room. The session's own agent is never one (a
  self-dispatch is refused and its drive lock is held by this turn), so a
  room with one agent answers that no other agent can try it; the builder
  then role-plays and says so.
- **Expiry.** Each retention pass deletes expired drafts
  (`RetentionStore.PurgeExpiredDrafts`) before the blob collection;
  `ReferencedBlobKeys` keeps a draft's bundle while its row exists.

## The dispatch route (realtime)

`POST /internal/v1/rooms/{room}/dispatch` (`svcauth.RequireSigned`) with
`wire.DispatchRequest {parentJobId, recipient, text, expert?, draft?, clientMsgId?, role?}`
(`expert` is a `wire.ChatExpert {id, version?}`).
The gateway's public door `POST /v1/rooms/{room}/dispatch` is the only edge
route rewritten onto an internal one. `RouteKey` keys the path to the room,
so the post (which touches the room's hub) runs on the room's owner replica.

**Guards** (refusals answer `{code, error}`; every decision, admitted or
refused, is an `extension_audit` row of kind `agent_dispatch` with the
bridge owner as actor and the parent job, requester, new job and depth in
the detail):

| Guard | Refusal |
| --- | --- |
| parent job exists in this room | 404 `parent_not_found` |
| parent is claimed / running / reflowing and leased | 409 `parent_not_running` |
| the lease is held by a bridge of the caller (`BridgeOwner`, cross-tenant by bridge id) | 403 `not_parent_bridge` |
| the requester is still a human member of the room | 403 `requester_not_member` |
| depth ≤ 3 (`wire.MaxDispatchDepth`) | 409 `depth_exceeded` |
| recipient ≠ the parent's agent (an empty core matches any core) | 409 `self_dispatch` |
| recipient not an agent higher up the parent chain | 409 `dispatch_cycle` |
| ≤ 8 persisted dispatches per parent (`wire.MaxDispatchFanOut`) | 409 `fan_out_exceeded` |
| not the people chat | 400 `people_chat` |
| the expert, if any, is usable by the requester (`ChatPost.Expert`) | 400 `expert_not_available` |
| the draft, if any, was staged by the parent job in this room and has not expired (`ChatPost.Draft`) | 400 `draft_not_available` |
| the dispatching member's chat rate limit (gateway) | 429 |

The fan-out count and the post are serialized per parent job (striped
mutex, no per-key state; every dispatch of a parent lands on its room's
owner). A `clientMsgId` retry answers the original job. The depth and
fan-out limits are protocol safety constants, not deployment data.

**Attribution.** The message's author is the parent job's requester;
`payload.dispatchedBy {jobId, parentSeq, depth, via, role?}` marks it
(`parentSeq` is the room sequence of the parent job's request; the feed
shows "maya → via jack/codex → bob/claude", the team role as a label,
plus "(as <expert>)" from the message's `payload.expert`; the audit
detail carries `role=`), and the job persists `dispatched_by_job_id` /
`dispatch_depth` (run records show `dispatchedBy`).

**Nesting in the feed.** Every agent row a job writes (the bridge's
replies and notices, a lead's progress lines, activity's failure notice)
carries `payload.jobId` and `payload.sourceSeq` (the job's request). The
web groups a turn from these alone (`web/src/views/room/feedTurns.ts`,
`layoutTurns`): the request, then one collapsible block ("Team · writer,
critic", or "Delegated to bob/claude" for plain dispatches) holding each
dispatch with its replies, notices and nested dispatches (indented per
depth) and the lead's progress lines, then the lead's own replies. A live
feed and one rebuilt from history group the same way; a row whose request
is not loaded stays in place until "Load earlier" brings it in. The block
is open while a dispatched job runs (from the room's job list) and folds
when all have settled.
