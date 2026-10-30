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
realtime POST /internal/v1/rooms/{room}/dispatch   (any replica)
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
  when the session is created on an `agentProfile` core, every session,
  with or without an expert or skill packs (a session wearing nothing still
  gets a profile: the guidance and the contributors' servers), so a plain
  follow-up after an expert's turn has the tools too: the server mints a
  random bearer and the session's profile gets the server, either
  - `http` with an `Authorization: Bearer …` header, when the target
    advertises http MCP (`core.Target.MCPTransports`), or
  - the stdio shim `bnbloc-bridge roomtools-stdio --url <endpoint>` with the
    bearer in `BNBLOC_ROOMTOOLS_TOKEN` (env, never argv). ACP requires every
    agent to accept stdio servers.
  Bearers are looked up by hash and compared in constant time; the release
  hook revokes a bearer when the bridge drops the session binding (rebind,
  forgotten room, core endpoint change), and a bridge stop revokes them all. A core whose
  capabilities cannot be read at the drive (a protocol daemon's failed
  discovery, `core.CapabilitiesKnown`) is not driven without them: the job
  fails `core_unavailable` and is retried, rather than running a whole
  session with no tools.
- **Bound to the current job.** The turn hook binds the bearer to the job
  each turn drives. A bearer only ever dispatches on behalf of that job, and
  realtime admits the dispatch only while the job is running on a bridge of
  the caller, so a bearer that outlives its turn dispatches nothing.
- **The agent never sees the bridge's session.** Every gateway call is made
  by the bridge's own authenticated client; the bearer is meaningless
  outside the bridge process.
- **The room relay** (`relay.go`, `/relay/<gateway path>`): the same
  server, the same bearer, for MCP tool components whose credentials are
  the room (`mcp.credentials: "room"`, content-components.md). The bridge
  sets `BNBLOC_ROOM_RELAY_URL` and `BNBLOC_ROOM_RELAY_TOKEN` on those
  servers only (`SessionInfo.RoomRelayServers`). A relayed call reaches
  only `wire.RoomRelayRoutes` (routes that admit a job grant; a `{id}`
  segment matches one path segment); the relay sets `?room=&job=` from
  the bound job, so it acts as the turn's requester and is refused (409)
  between turns. A relayed call has no fixed time limit: it is bound to
  the tool's request alone (`RawBody.Relay`, the bridge's client without
  a total timeout), so a slow picture or a video's publish finishes, and a
  stalled gateway ends with the agent's tool call
  (`docs/prd/2026-10-19-video-generation.md` §2.4).
- **Host tools** (`hosttools.go`, `services/bridge/hosttool`): the same server
  fronts an MCP tool placed `"host"` at `/tools/<name>` (content-components.md
  "mcp.stdio.placement"). The bridge starts the tool as a host process at the
  first request (one per session and tool, in the session's directory, with its
  settings and the relay, without the bridge's own `BNBLOC_*`), speaks MCP over
  stdio to it, and answers the agent's initialize itself. The session's bearer
  is the only way in; a call that acts needs the turn bound to the session (the
  handshake and the lists do not); revoking the session stops the tool.
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
  aborting the matching request. The `initialize`/`server/discover`
  `instructions` field is one line pointing to the session guidance (below):
  how to use the tools is told once, in the session's profile, and each
  tool's description is in tools/list, so a harness that surfaces MCP
  instructions shows neither twice.
- **Tools are listed in brief** (`roomtools/brief.go`). `tools/list` rides every request of a session as a cached prefix, so a tool's size there is paid by every turn whether it is used or not. Each tool lists a one or two sentence brief (what it does and the constraint that must never be missed: "only after the person confirms", "wipe … only when the person asked"), its parameters with names, types, enums and bounds, and a note only where a name does not explain itself. The full description and each parameter's note stay in the tool's definition and are returned by `room_help`; nothing is cut. A tool without a brief is refused at registration (`TestEveryToolHasABrief`), so a new tool is short by default, and `TestBriefKeepsTheSchemaShape` pins that a brief changes words, never the schema. Every tool stays listed and callable by name: nothing depends on a client supporting a changing tool list. The session guidance (`roomlog.SessionGuidance`) carries no tool's rules, only what is true of all of them, and one line that the person sees none of the tools or files. **One tool per domain** ([docs/prd/2026-10-20-fewer-sharper-room-tools.md](../prd/2026-10-20-fewer-sharper-room-tools.md)): a domain's actions are one tool with an `action` (`components`, `app_data`, `room_setup`), whose brief is one sentence and the action names; each action's parameters and rules are in `room_help <tool>`.
- **Tools** (one registry: dispatch, step 2; content authoring, step 5;
  interactive apps' data, `app_data`; the room's setup, `room_setup`).
  The full tools/list is pinned by `roomtools/testdata/tools_list.json`;
  every number a description states comes from its constant:

| Tool | Reads / writes |
| --- | --- |
| `list_room_agents()` | `GET /v1/rooms/{room}/agents` (the live roster); handles `owner/provider`, or `owner/core/provider` where one owner exposes a provider on two cores. A team lead's turn adds `team`: who takes each role this turn, or why nobody can. |
| `ask_agent(handle, task, expert?, wait_seconds?)` | A team lead may pass a role of its roster as the handle (below). `POST /v1/rooms/{room}/dispatch` with the bound job as parent, then polls `GET /v1/rooms/{room}/jobs/{jobId}` (default 10 min, at most 20, below the bridge's turn bound, `BNBLOC_BRIDGE_MAX_TURN`, 2 hours by default). Settled: the reply text (the run's session replies in the channel history, up to the run's finish), fenced as the agent's data (`roomlog.Fence`), and the deliverables `GET …/artifacts?sourceSeq=<dispatch seq>` as `[{name, kind, artifactId, version, link}]` (`read_artifact` takes the `artifactId`; a session without `.room/` files has no other source of it). Still running: the handle. |
| `room_status(handle?)` | The run record of one task this session dispatched (with reply and deliverables once settled), or all of them. |
| `read_room(since?, limit?)` | History of every channel an agent may read (`roomlog.History`: the room's channel listing, never the people chat), people's messages and agents' replies, notices skipped: one fenced `transcript` of record lines (`#<seq> ` + `roomlog.Line`, attributed like `.room/`), `latestSeq`, `more`. |
| `components(action, …)` | The Library of the job's requester as one tool ([docs/prd/2026-10-20-fewer-sharper-room-tools.md](../prd/2026-10-20-fewer-sharper-room-tools.md); `roomtools/components.go`): its brief is one sentence and the action names; `room_help components` gives each action's parameters and rules. Each action keeps the parameters, permission checks and result of the tool it replaced (below); a parameter of another action is refused naming the action that takes it, before any call. |
| `components` `save` (dir \| file \| artifactId \| kind, id?, name?, category?, tags?, instructions?, include?) | With `kind: "room"`: this room as a room component in the requester's Library (`roomtools/room_save.go`): `GET /v1/rooms/{room}/component-draft?job=&room=` (the same capture as the room's "Save as component", `roomDraft`: the room's owner or admins only, the route's 403 otherwise), laid out as the content form with the listing the agent writes (name, displayName, description, category, tags; the draft's where it gives none), every part the draft holds, the room's own asks for sources and the MCP tools its runs had (with their permissions) included, and what the agent and the person agreed: `instructions` (the room's instructions; the room's expert is then left out, which the result says, since they become the installed room's expert) and `include: {apps, files}` (apps made here and room files by artifact id, sent as the save's `wire.RoomInclude`; the gateway reads them from the room), then `POST /v1/content-components` (or `…/{id}/versions`) with the form, as the dialog saves: one validator ([docs/prd/2026-10-20-complete-room-capture.md](../prd/2026-10-20-complete-room-capture.md) §6). With `artifactId` (any deliverable of the room): posts to `POST /v1/rooms/{roomId}/artifacts/{artifactId}/publish-work?job=`, which snapshots the deliverable's current version (a site's files or the one file, a cover, never its data) with the recipe captured from the job that produced it as a work of the job's requester, created as `name` with `category` or, with `id`, as that work's next version ([content-components.md](content-components.md), "Works"); the result carries the recipe's summary (`recipe`) and the note tells the agent to say the work carries it. Otherwise it reads every file under `dir` (workspace-relative, e.g. `drafts/reviewer`) through the core's session-files seam (`core.SessionFiles`, handed to the tools per turn as `TurnInfo.Workspace`), packs them (`tarbundle`, content limits) and posts the archive to `POST /v1/content-components` (or `…/{id}/versions`) with the bound job's `?job=&room=` grant: the gateway validates it and saves it as the job's requester. The validator's message comes back as the tool error. A core without session files cannot save; the agent points the member at "Save as expert" under its reply's files. When the organization uses the component, the gateway keeps the new version with its author (`orgProposed`: a job's grant never takes content to the organization) and the tool puts the same publish card as `components publish`'s for that version in the room; the note tells the agent it reaches the organization only by the person's click. Every granted save puts a transient `content_saved` event in the room, which the member's workshop toasts. |
| `components` `get` (id, version?) | `GET /v1/content-components/{id}[/versions/{v}]` with the grant: the manifest and every text file, as the requester may read it (the "Improve" flow). Files of another author (a shared copy's included) are fenced as that author's data. |
| `components` `list` (kind?) | `GET /v1/components` with the grant (`roomlog.ReadLibrary`): the requester's whole Library, most recently updated first: experts, skill packs, prompt packs (`kind` `expert`, `skill`, `prompt`) and MCP tools (`mcp`), with version, scope, `mine` and the update time. |
| `read_artifact(artifactId, path?, version?)` | A deliverable of the session's room (below), through the room's own artifact routes; a site's file through `GET …/versions/{v}/files/{path...}?job=` on the main origin. |
| `components` `publish` (id, scope, note?) | For every kind (an app and a room included). `org` never shares by itself: it reads the component with the job's grant (`GET /v1/content-components/{id}`; another author's, a shared copy too, is refused) and puts a card in the room, a notice row of kind `publish-proposal` whose `payload.proposal` (`wire.PublishProposal`: the component, its latest version, the organization, the agent's note, `for` the requester) the web shows only to the requester; it is shared when they click Publish, which calls `POST /v1/content-components/{id}/publish` as them (a publish request an organization admin approves, or published at once where the organization requires no approval, `contentshare.NeedsApproval`). The gateway refuses a job's grant on an `org` publish (403, `errJobWidens` in `publishToOrg`, which also keeps a version a job saved from the organization), so text an agent read can never share a member's work. The card names its version and the click publishes that one. `personal` posts to the publish route with the grant and withdraws it at once; `platform` is answered locally as a platform admin's action ([content-components.md](content-components.md), "Sharing and publishing"). |
| `room_setup(action, …)` | The room's setup, as the job's requester ([docs/prd/2026-10-20-room-setup-by-conversation.md](../prd/2026-10-20-room-setup-by-conversation.md); `roomtools/room_setup.go`), on the routes the room's dialogs use: proxied ones through the gateway's agent door (`?job=`, `services/gateway/agent_door.go`), the gateway's own through the content grant (`?job=&room=`). `get` reads `GET /v1/rooms/{room}` (description, welcome, quick prompts), `…/channels`, `…/schedules` and `…/groups`, `GET /v1/content-uses?room=` (the room expert and packs) and `GET …/sources` (its asks and where each stands); text people wrote comes fenced as data. `set` is `PATCH /v1/rooms/{room}` (the description only: the door refuses any other field; the room service lets only the owner) and `PATCH …/welcome` (the welcome and quick prompts, the owner or admins). `use`/`unuse` find an expert, skill or prompt pack by name in the requester's Library (`roomlog.ReadLibrary`) and `PUT`/`DELETE /v1/content-uses` (scope `room`, pinned at the version they see, an expert then `room-expert`: the owner or admins). `channel` without an `id` is `POST …/channels` (where the room's creation policy lets the person put one, `mayPlaceChannel`) and then a `PATCH` of the rest it was given; with an `id` it is `PATCH …/channels/{id}` (name, purpose, rules, section, position and the settings document, which the tool sends whole: the channel's current one with the given fields changed; a default agent is a `list_room_agents` handle resolved to its key by `resolveHandle`, refused when it names none or two) and `POST …/channels/{id}/archive` or `…/restore` (`mayManageChannel`: the room's owner and admins, the channel's owner, its section's owners); the door refuses a channel patch naming `ownerId`, and deleting a channel, sections and following are not door routes (docs/prd/2026-10-28-channels-and-threads.md §2.6). `set` also takes the room's `rules` and `recommendedChannels` (`PATCH /v1/rooms/{room}` with those two only: the door admits a room patch of the description alone or of these alone, never the agreement switch or the creation policy). `add_schedule`/`remove_schedule` are `POST …/schedules` (on a clock or, recurrence `event`, when the platform changes, `name` limiting `component.published` to one component ([schedules.md](schedules.md)); an AI task, a notice, or with `agent` (a handle from `list_room_agents`, resolved by `resolveHandle`) a request posted to that agent as the requester each time; the door refuses any other recipient and a request to an agent that is not one of the room's agents online now, realtime's roster through `roomRoster`, the agents the room's Schedules dialog offers the person: `agentScheduleTarget`; another member's agent is a borrow, authorized at drive time) and `DELETE …/schedules/{id}` (the requester's own), with every check a schedule route makes for a person (membership, the 20 per member and 50 per room caps, the assist budget at run time). `ask_source` is `POST …/sources/{source}` (`{purpose}`, the owner or admins, `askRoomSource`: waiting for the organization's admin like a package's ask). Each change spends the requester's cost budget and is audited once the route answered, allowed or refused (`room-setup`, the room, the agent and requester, never the text; an ask as `source`), and shows in the room as a notice line naming who asked ("maya's agent set the welcome", kind `room-setup`, under the job's progress session), which a person undoes in the room's settings. The brief says: propose each change in a sentence and make it only after the person agrees. |
| `room_help(topic?)` | A tool's full instructions (its description and every parameter's note) by name; without a topic, the tool names. See "Tools are listed in brief". |
| `post_progress(text)` | Posts one progress line of the bound job as a room notice (below): at most 20 per job, 500 characters each. |
| `components` `try` (dir, task, handle?, wait_seconds?) | A real quick try of an unsaved expert or skill pack (below): packs `dir` like `components save`, validates it with `contentpkg` (the error goes back to the agent), stages it with the job grant (`POST /v1/content-drafts`) and dispatches `task` to another agent wearing it through the same door, guards and wait as `ask_agent`. |
| `app_data(action, artifactId?, collection?, id?, filter?, sort?, limit?, after?, doc?, inc?, public?, ifAbsent?, ifVersion?, confirm?)` | An interactive app's data (below): the gateway's room entry `/v1/rooms/{room}/apps/{artifactId}/…?job=` as the job's agent, one of the app's owners. `list` without `artifactId` is the room's apps (`roomlog.ReadApps`), with it the app's `stats`; `find`, `get`, `insert`, `put`, `patch`, `delete` map to the app data API; `wipe` (`DELETE …/data`) only with `confirm: true`. |

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
targets advertise `mcpTransports` (and get these tools) or not. Model-facing
text has one owner per category
(`docs/bugs/2026-10-13-model-facing-text-review.md`):

- **Session guidance, once per session** (`roomlog.SessionGuidance`, one
  `go:embed` template, `internal/roomlog/guidance.tmpl`, rendered with
  `roomlog.Guidance` facts: `Self`, `HasTools`, `HasRoomFiles`,
  `TeamLead`; the dispatch limits come from `wire`). It says who the agent
  is and how a record line reads; that a turn answers the
  `[now — from <sender>]` message alone, that a records header marks
  background and a fence marks someone else's text; the room model (every
  agent works in its own workspace; deliverables are stored by bnbloc and
  named by their producer, with a read-only copy of each in the reader's
  `.room/deliverables/` where bnbloc writes `.room/` (to change one, copy
  it into its own files first), never its working files; the
  Library is bnbloc's Library of the person who asked, shared by every agent
  and core in the room; how to verify); to answer in the person's language;
  with `.room/` files, what each holds; with these tools, how to use them
  (no per-tool list: tools/list carries it); for a team lead, a default rule
  for leading its SOP may override. It never rides a turn's prompt: the
  bridge's `sessionGuidance` (`services/bridge/profile.go`, the one owner of
  where session guidance goes, and of its facts: a contribution declares
  `RoomTools`, contributors word nothing) puts it at the head of the agent
  profile the session is created with on a core advertising `agentProfile`
  (a stable prefix providers cache; outside the binding sum), else at the
  head of the session's first prompt, through the expert fallback's prefix,
  in an empty room too. `.room/README.md` (`roomlog.ReadmeFile`) is the same
  text in a pull workspace, written with the facts the session was created
  with, only when it differs from what the workspace holds
  (`roomlog.ReadmeUpdate`, `CtxState.ReadmeSum`).
- **Scaffolding** (`internal/roomlog/markers.go`): `RecordsHeader` heads
  every records file and section, `Fence(author, text)` wraps every text
  someone else wrote (a tag derived from author and text: the text cannot
  close its fence, and goldens and prompt caches stay stable), `Line`
  renders a record line with its attribution (`jack/codex for maya →
  jill/claude: …`), and the `[now — from <sender>]` marker opens the message
  a turn answers (the sender as the history records it, else the job's
  requester). `roomlog.Truncate` is the one rune-safe truncation of
  model-facing text. `TestModelTextMarkersSingleOwner` forbids these
  literals (and the identity line) in any other Go string.
- **`.room/artifacts.md`** (`roomlog.ReadDeliverables`,
  `DeliverablesFile`): the room's `roomlog.MaxListedDeliverables` newest
  deliverables, each with kind, latest version, id, producer (the reader's
  own: "from you"; another agent's: its source is in that agent's
  workspace) and where its copy is, or why it has none. The producer of a
  version is `roomlog.VersionProducer`: its source job's agent from the run
  record (`owner/provider` and core), else its author (a person's upload).
  The bridge caches resolved versions (`roomlog.ProducerCache`; a version
  never changes), so a turn costs one listing.
- **`.room/deliverables/<id>/`** (`roomlog.CopyDeliverables`,
  `docs/bugs/2026-10-13-agent-loses-its-workspace.md`): a read-only copy of
  each listed deliverable's latest version, a site as its file tree,
  anything else as one file (its file name). Written in the pull pass
  before the records that name it, and only when the version changed since
  the workspace's last pass (`roomlog.CopyState`, kept with the agent's
  workspace, which outlives its sessions: `services/bridge/workspaces.go`).
  A version over `roomlog.MaxDeliverableCopyBytes` (8 MiB) is not copied
  (artifacts.md says so, with its size); one pass writes at most
  `MaxDeliverableCopyPassBytes` (32 MiB) and the rest follow on the next
  turns. Nothing under it is deleted, so a site version that dropped a file
  leaves the old file in the copy. Whatever happens to an agent's
  workspace, it can recover a deliverable's source from here, and an agent
  without the room tools reads other agents' work. `.room/` is
  bnbloc-owned: never published, synced or packed, write-denied in
  sandboxed drives.
- **`.room/library.md`** (`roomlog.ReadLibrary`, `LibraryFile`): the
  requester's Library read with the turn's job grant, the
  `roomlog.MaxLibraryItems` most recently updated (name, kind, version,
  scope, updated time). The gateway's visibility owners decide what is in
  it (content-components.md, "The Library as an agent reads it").

- **`.room/live.md`** (`roomlog.LiveFile`): what members did inside
  running deliverables since the bridge started, each gesture's live state
  bounded (`LiveGesture.BoundState`) and the trail fenced (a deliverable's
  code wrote it).
- **The platform's contract files** (`roomlog.ContractFiles`,
  `ContractsUpdate`; docs/prd/2026-10-20-no-built-in-content.md §2.5),
  written at a workspace's first pass and again only when one changes, at
  the public paths of `component`: `.room/app-sdk/sdk.js`, the interactive
  apps' SDK in demo mode (`appsdk.DemoJS`: every call answered in the
  page's memory, nothing sent; the `interactive-apps` skill's self-test
  serves it as the page's `~app/sdk.js` before the agent says an app
  works, `docs/prd/2026-10-13-interactive-apps-self-test.md`);
  `.room/app-sdk/sdk.d.ts`, its declarations (every call, its options, the
  caps); `.room/content-limits.md`, the content validator's bounds
  (`contentpkg.LimitsDoc`). Content refers to them instead of copying
  them.
- **`.room/apps.md`** (`roomlog.ReadApps`, `AppsFile`): the room's
  interactive apps, each with its producer and its collections' document
  counts, never a document's content. The apps are the listed deliverables
  (`roomlog.MaxListedDeliverables`) the listing marks `isApp` (the
  server's one rule, `store.Artifact.IsApp`: a site that loads the app SDK
  or whose data space exists), each with its owners' `GET
  …/apps/{id}/stats?job=` read with the turn's job grant; `app_data` takes
  any site by id. The file appears only once the room has an app; from
  then on the session's workspace gets it every turn (`roomlog.AppsUpdate`,
  `CtxState.AppsListed`), so it never claims an app that is gone. INDEX.md
  names it.

The `.room/` tree is the agent's workspace's, shared by its session of
every channel (`docs/prd/2026-10-13-sessions-per-channel.md`): one set of
records, written by whichever session's turn runs (turns on a workspace
never overlap), each pass continuing what the workspace holds.
The bridge rewrites `artifacts.md`, `library.md`, `live.md` and (once
written) `apps.md` every turn (`roomRecordFiles`, beside the history:
their data changes); a read that
fails renders as unreadable, never as empty, and never fails the turn. The
per-turn pull prompt (`roomlog.ComposePointer`) is the message alone (its
attachments, `[now — from <sender>]`, its text), plus one line
`(room records: .room/INDEX.md)` on a core without `agentProfile`, whose
guidance rode a first prompt a harness may compact away. Every surface is
pinned by a golden (`internal/roomlog/testdata/golden/`, the tools list,
the team section, the prompt prefix) and bounded by `TestPromptBudgets`
(`services/bridge/prompt_test.go`). `.room/` is a bnbloc-owned path
(`core.BnblocOwnedPath`): never a deliverable, synced or packed.

**`read_artifact`** (`services/bridge/roomtools/artifact.go`) lets an agent
with tools check another agent's work: the artifact detail and version
routes (`GET /v1/rooms/{room}/artifacts/{id}[/versions/{v}]`) as the bridge's
session, so the room service refuses a non-member and an artifact of
another room (the tool also refuses a detail naming another room); a blob
version through its content route; a site's file (`path`, default
`index.html`) through the gateway's site file read on the main origin,
`GET /v1/rooms/{room}/artifacts/{id}/versions/{v}/files/{path...}?job=`
(`services/gateway/site_file_read.go`). That route admits the bound job's
grant (the job runs on this bridge, its requester may still post), reads
the version as the requester through the room service (a non-member or
another room's artifact is a 404), answers the bundle file exactly as
stored (no rewrite, no fallback) and refuses a file over 4 MiB with a 413.
The tool never opens a site ticket: a site URL may be on the sites origin
(`BNBLOC_SITES_ORIGIN`), which never sees a session
(`TestRoomToolsNeverOpenSiteTickets`). A stored non-site version over 4 MiB
is not downloaded; the text returned is capped at
64 KiB (`truncated`), binary content is not shown, and the text is fenced
(`roomlog.Fence`), naming its producer, as data to check, never
instructions.

**`app_data`** (`services/bridge/roomtools/appdata.go`,
`docs/prd/2026-10-12-interactive-apps.md` §4.6) reads and changes an
interactive app's data: collections of JSON documents the people using a
site deliverable wrote through the app SDK. Every call goes to the
gateway's room entry of the app data API with the bound job's grant
(`roomlog.AppPath`: `/v1/rooms/{room}/apps/{artifactId}/…?job=`); the
gateway admits it only while the job runs on this bridge and makes the
job's agent (`agent:<owner>/<core>/<provider>`) the participant, one of the
app's owners: it reads every document with its real `_author` and may
change any, and what it writes is stamped as the agent. The room service
applies the platform's rules; the tool adds only `wipe`'s `confirm`. A
write's conditions travel as the API's headers (`ifVersion` is `If-Match`,
`ifAbsent` on a put `If-None-Match: *`; the bridge's gateway client sends a
`RawBody`'s `Header`). Every document returned is one JSON line inside one
fence (`roomlog.Fence`, written by the app's users), at most
`maxAppDataText` (96 KiB) per call: a find stops before the document that
would pass it and returns `next` there, a single larger document is cut
(`truncated`). A refusal reaches the agent with the API's reason
(`refusal`).

**Budgets.** `TestPromptBudgets` caps the session prefix: guidance 745
tokens (825 for a team lead), tools/list 1,790, guidance plus tools/list
2,520. The room tools' two budgets are their last measured size, rounded
up to the nearest 10, and only ever lowered: a new tool or action fits by
merging or shortening, never by raising them. On 2026-10-20 the five
content tools became `components` and `room_setup`'s brief one sentence
and its actions: tools/list 2,351 → 1,787, the prefix 3,090 → 2,520.
Earlier: `app_data` added 345 tokens to tools/list (2,168 → 2,513) and its
guidance line 39 to the guidance (619 → 658). The deliverable copies'
line added 10 (658 → 668; a team lead's 740 → 750), paid for by a shorter
workspace line.

## Tool calls on the run record

Which tools earn their place is measured, not guessed
([docs/prd/2026-10-20-fewer-sharper-room-tools.md](../prd/2026-10-20-fewer-sharper-room-tools.md) §2.3).
The server records every `tools/call` a session makes while it is bound
to a job (`roomtools/calls.go`): the tool, the action when the tool takes
one (only a value of its `action` enum, never text the agent wrote) and
the outcome, `ok`, `refused` (the tool's own check or a gateway 4xx: the
call as made was refused) or `error` (a gateway 5xx, a transport failure,
an unreadable answer: `Result.failed`). Never the arguments or the result.
A run keeps its first `wire.MaxRunToolCalls` (200) calls and counts the
rest (`callsDropped`); the server holds at most 256 jobs' calls waiting.
The job's outcome takes them once (`Server.TakeCalls`, through the
bridge's `roomCalls`), an aborted job's are dropped, and the bridge's one
writer of `wire.RunDiagTools`, `withTools`, puts them on the outcome's
report beside the session's MCP tools (`wire.AgentRunTools`: `mcp`,
`calls`, `callsDropped`). A call made with no bound job belongs to no run
and is not kept; an unknown tool name is a protocol error, not a call.

Org admins read the sums at `GET /v1/tenant/jobs/tool-calls?from=&to=`
(activity, `tool_use.go`; manage_org like every `/v1/tenant/*` path; the
listing filters narrow it): per tool and action, calls, refused and
errors over the runs created in `[from, to)` (default the last 7 days),
most called first, at most `wire.MaxToolUseRuns` runs read (`truncated`
and `oldestMs` say where it stopped) and the calls runs did not keep
(`dropped`). The web shows it under Team, Runs, "Room tool use" (7 or 30
days), read only when opened.

## Teams

A turn wearing a team expert ([`content-components.md`](content-components.md),
"Expert teams") binds its roster to the session (`roomtools.Job.Team`:
each role's agent or why none, and its resolved expert or why none). The
guards hold in the tool layer (`services/bridge/roomtools/team.go`):

- **Roles.** `ask_agent(handle: "<role>")` dispatches to the role's agent
  wearing the role's expert (`expert` = its `id@version`) with
  `role` on the dispatch. A handle without "/" that is no role is refused
  as not covered by the roster; an unstaffed role or one whose expert does
  not resolve names why; a role refuses an explicit `expert`. A lead may
  still name an agent handle.
- **Staffing is live, never in the instructions.** The lead's roster
  section (`expertteam.Section`) is content only: role → expert, the roles'
  descriptions fenced and bounded (`MaxCoversBytes`), the mechanics. Which
  agent takes a role changes as agents come and go, and the instructions
  feed the session's binding sum, so `list_room_agents` reports it from the
  turn's bound roster and the lead keeps its session
  (`TestRoleAgentOnlineChangeKeepsLeadSession`). How to lead (plan,
  delegate, never do a missing role's work) is the guidance's default rule
  (`TeamLead`), which the team's SOP may override.
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

## Quick tries of drafts (`components try`)

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
route rewritten onto an internal one. Any realtime replica serves it: the
post reaches the room's streams on every replica over the bus.

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
