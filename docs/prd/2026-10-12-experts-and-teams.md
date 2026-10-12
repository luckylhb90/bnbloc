# Content components: experts, skill packs, prompt packs; room tools and expert teams

Status: approved, in development (2026-10-12): steps 1 and 2 implemented,
step 3 implemented, step 5 implemented for experts and skill packs, step 6
expert teams and MCP tool components implemented, step 4 sharing and
approval implemented (see "Status" below). Feasibility verified in code and by experiments (see
"Evidence").

## Why

Members want to hire a specialist for a task, reuse a capability across
agents, and share good prompts. bnbloc runs any agent (coding, office,
general, digital-employee) on members' machines, on platform hosts or
behind a protocol adapter. So these must be bnbloc-level, agent-neutral
packages, not features of one agent runtime: one expert, skill or prompt, on
any agent and any member's machine, with borrowing, quotas, audit and
sandboxing already around every run.

Today the Components system installs executable extensions (cores,
plugins, sandboxes, previewers, notifiers, auth providers): signed offline,
installed by a super admin. This design **extends that seam once** with
**content components**, packages that are mostly text, which members can
author too, instead of building a separate library system beside it.

| Kind | What it is | Who can publish |
|---|---|---|
| **Expert** (`bnbloc.expert.package/v1`) | A role: always-on instructions plus skills, tools, prompts and display data. A **team** is an expert with a roster. | members and the platform |
| **Skill pack** (`bnbloc.skill.package/v1`) | One or more capabilities (`SKILL.md` + scripts + references) an agent loads on demand. | members and the platform |
| **Prompt pack** (`bnbloc.prompt.package/v1`) | A curated, versioned set of prompts with variables, used from the composer. | members and the platform |
| **MCP tool** (`bnbloc.mcp.package/v1`, executable) | A tool server an expert or skill needs: a stdio binary distributed like a sidecar, or an http endpoint. | the platform only |

Two capabilities ride on top:

- **Room tools:** agents see the room's agents and dispatch work to another
  agent (`ask_agent`) under the human requester's authority.
- **Expert teams:** a lead expert and a roster, dispatching through room tools.

## 1. The content component framework

### Package envelope (shared by every content kind)

```
<kind>/
  component.json   kind, name, version (semver), displayName, description,
                   category, tags, icon,
                   requires: { skills: ["name@^1"], prompts: ["name@^1"],
                               mcp: [{name, env: [names]}],
                               permissions: ["scripts", "mcp.stdio", "mcp.http:<host>"] },
                   + kind-specific fields (below)
  ...kind files...
```

Validation is per kind: paths, sizes (a `tarbundle` limit profile per kind),
the manifest schema, declared permissions and references. One validator
package backs install, member publish and the conversation builder.

### Sources, scopes and trust

Executable kinds stay as they are: platform-signed, super-admin installed.
Content kinds add a **member source**. Its trust anchor is server validation,
a pinned content hash and an approval record instead of an offline
signature.

| Scope | Who sees it | How it gets there |
|---|---|---|
| Personal | the author | save (validated) |
| Shared copy | named members or a room | the recipient gets a copy pinned to that version; later edits by the author don't change it |
| Organization | the org | an org admin approves a pending request (`manage_org`, the request/approve pattern of quota requests) |
| Platform | every org (rollout + tenant scope as today) | a super admin publishes it signed (`bnbloc cores install --package`, the existing pipeline) |

The **rollout model is unchanged** and applies to content too: auto, opt-in
or off, plus tenant scope. A member's opt-in is a `component_installs` row,
as for every kind today.

### Storage

- Executable components keep their `platform_settings` records (few, large,
  read at boot).
- Content components can be numerous (every member's skills and prompt
  packs), so they get their own tables:
  - `content_components (id, kind, name, tenant_id, owner, scope, status, latest_version, created/updated)`;
  - `content_component_versions (component_id, version, manifest_json, bundle_blob_key, sha256, signature?, created_by, created_at)`.
- Bundles are tar files in the blob store, addressed by sha256 (added to
  `ReferencedBlobKeys`; `artifactEntitled` grants a bundle to the members who
  may use it). Versions are immutable.
- **One visibility function** (scope + rollout + tenant + opt-in) answers
  "may member M use component C" for both registries, so the member
  `GET /v1/components` lists executable and content components through the
  same filter. Today's `memberComponentVisibility` is extracted and reused,
  not copied.

### Lifecycle (every content kind)

- **Create** in one of three ways:
  - by conversation, through the builder (section 5);
  - in a form in the Library;
  - by **import** from existing formats: a skill directory, a Claude
    Code-style plugin, or a selection of prompts.
- **Version:** each save is a new immutable version. Rooms and members pin a
  version or follow the latest; an update notice shows when a pinned
  component has a newer version.
- **Dependencies:** an expert references skill packs, prompt packs and MCP
  tools by `name@range`. They are resolved at use time against what the
  member may use; missing ones show on the card ("needs: …") instead of
  failing mid-run.
- **Discover:** one Library with Experts, Skills, Prompts and Tools tabs.
  Each has search, categories, author, version, declared permissions and
  usage counts (runs, members).
- **Retire:** unpublish hides a component from new use; pinned users keep
  their version until they move.

### Running untrusted content safely

An expert's or skill's scripts and stdio MCP servers run on the machine of
whoever uses them:

- Declared permissions show on the card and are confirmed on first use.
  They are approved at org and platform scope.
- Content that is not the member's own and declares `scripts` or `mcp.stdio`
  **forces a sandboxed drive** (the existing `decideSandbox` /
  `sandboxRequired` path), or is refused where no sandbox is available.
- stdio MCP servers come only from MCP tool components (platform-signed, hash
  pinned, supervised by the desktop like sidecars). Content references them
  by name.
- Secrets never ship. Requirements declare env names; values come from the
  member's local settings (like plugin `localSettings`) or an org-shared
  connector.

## 2. Expert (and team)

```
expert/
  component.json   + quickPrompts[{title, prompt}], welcome,
                     team?: { lead, members[{role, expert, defaultAgent}] }
  INSTRUCTIONS.md  persona and method: the always-on part
  skills/<slug>/   optional embedded skills (or reference skill packs)
```

An expert names no model and grants no permission of its own. The model is
the member's or platform agent's choice; tool reach stays with the core, the
borrow grant and the sandbox.

### How a core wears it (verified per agent)

A new optional Core Protocol capability, `agentProfile`, on `CreateSession`
carries `{instructions, skills: [{name, files}], mcpServers (ACP shape), env}`
(secrets never logged or persisted). Each core maps it natively:

| Core form | Instructions | Skills | MCP |
|---|---|---|---|
| Claude Code (daemon or ACP) | `--append-system-prompt` / ACP `_meta.systemPrompt.append`; `CLAUDE.md` in cwd | `.claude/skills` (verified) | `--mcp-config … --strict-mcp-config` + allow-list (verified end to end) |
| Codex | `developer_instructions` / `AGENTS.md` (verified in the rendered prompt) | `.agents/skills` (verified) | `-c mcp_servers.*` (spawn verified) |
| OpenCode (ACP) | `AGENTS.md` (verified) | `.opencode/skills` (verified) | `opencode.json` (tool call verified) |
| Other ACP agents | `_meta` or env per agent | files where supported | ACP `mcpServers` |
| Manifest daemons | `{instructions}` + raw-JSON template vars | daemon-defined | daemon-defined |
| Any core without the capability | **fallback: the bridge prepends the instructions to the prompt** (every core, today) | summarized into the instructions | none |

- **bnbloc-acp:** maps the profile per agent from `agents.json`
  (`profile: {instructionsVia: "meta" | "env:<NAME>" | "file:<path>", skillsDir, mcp}`),
  so a new agent is configuration, not code.
- **Workspace file writes:** the `WorkspaceFileWriter` contract widens from
  `.room/` to a fixed list of agent-config paths (`CLAUDE.md`, `AGENTS.md`,
  `.claude/skills/**`, `.agents/skills/**`, `.opencode/**`, `.mcp.json`).
- **Those paths are protected:** they are excluded from deliverable
  publishing and code sync, and are write-denied in sandboxed runs. The
  workspace is bnbloc-owned per room × core × provider, never the member's
  repo. Files are re-materialized by checksum when the version changes.
- **The member's own setup still loads:** their global agent configuration
  loads alongside, so an expert adds a role rather than wiping their setup.

### Using it

- **Composer picker:** "Expert" + "runs on" (my agent / a borrowed agent / a
  platform agent).
- **`@reviewer`:** resolves to a pinned expert, run on the sender's default
  agent.
- **Room settings:** pinned experts, and an optional room expert applied to
  every agent turn.
- **Cards:** quick prompts and the welcome card come from the manifest.
- **Records:** audit and the job row read "expert X v1.2 on agent Y of
  member Z".

## 3. Skill pack

```
skill/
  component.json   + skills: [slug...]
  <slug>/SKILL.md  frontmatter (name, description) + body, scripts/, references/
```

- **Enabled at three levels:**
  - for a member (all my agents);
  - for a room (every agent in the room);
  - inside an expert (`requires.skills` or embedded).
- **Delivery:** `agentProfile.skills` delivers them. The core materializes
  them into the agent's native skills directory, and the agent loads them on
  demand from their descriptions. Cores without skills get the descriptions
  summarized into the instructions with a pointer to the files.
- **Import:** an existing skill directory, or the skills of a Claude
  Code-style plugin, imports as a pack with a generated manifest.
- **Scripts:** a skill with scripts declares `scripts`, so other members run
  it sandboxed (section 1).

## 4. Prompt pack

```
prompts/
  component.json   + prompts: [{id, title, body, variables: [{name, label, default?, options?}],
                                tags, slash?: "/name"}]
```

- **In the composer:**
  - the prompt picker;
  - `/name` slash commands;
  - variables filled in a small form before sending (`{{audience}}`);
  - starter prompts in a room;
  - an expert's quick prompts can point at a pack's prompts.
- **Cutover:** the prompt library (`prompts` table, `/v1/prompts`) becomes
  prompt packs:
  - every member's saved prompts are their personal pack;
  - "share with org" is the org scope;
  - org-admin moderation is the scope's approval and removal.
  - The separate prompts store, routes and moderation path are removed. This
    is a clean cutover, not a second library beside packs. **Decided
    (2026-10-12): start empty.** The `prompts` table is dropped, with no
    conversion (pre-release, no real usage to keep).
- **Usage counts:** carried over (`use_count` becomes per-prompt usage in
  the pack).

## 5. Authoring by conversation (all content kinds)

A built-in **builder** expert, published at platform scope, works like this:

1. It interviews the member: what the expert, skill or prompt pack should
   do, its method and tools, with examples.
2. It drafts the package into a visible `drafts/<name>/` directory of its
   workspace, so the draft shows as deliverables the member can read.
3. It tries the draft: it dispatches a sample task to an agent wearing the
   draft expert, or runs the skill.
4. It saves and publishes through two room tools:
   - `save_component(kind, dir)`: the bridge reads the files (session files)
     and posts them **as the requester** to `POST /v1/content-components`.
     The shared validator checks them, and a new version is stored.
   - `publish_component(id, scope)`: changes the scope. Org scope opens an
     approval request; platform scope is a super-admin action.

Revising ("make it stricter about tests") loads the current version and saves
a new one. The same flows exist as forms in the Library.

## 6. MCP tool (executable)

- A tool server that content can require.
- A stdio server ships as platform binaries with sha256 pins and is
  supervised by the desktop exactly like sidecars.
- An http server is an endpoint package (NetGuard + discover probe, as
  notifiers and auth providers do).
- Content references it by name. Members provide credentials through local
  settings or an org connector.
- This is the admin-approved tool catalog the safety rules rely on.

## 7. Room tools (agent dispatches agent)

**Where:** a loopback MCP server inside the bridge.
- It listens on 127.0.0.1 on a random port, takes a per-session bearer
  compared in constant time, and revokes it when the session ends. This is
  the desktop plugin host API pattern.
- The agent CLI runs on the host even for sandboxed drives (only shell
  commands go to the microVM), so it reaches the server.
- Platform agents run on a host member's bridge and get the same server.
- Attached through `agentProfile.mcpServers`: http with the bearer header, or
  a small stdio shim for agents without http MCP.

**Tools:**
- `list_room_agents()`: each agent's handle, owner, core, provider, online
  state, pinned expert and description.
- `ask_agent(handle, task, expert?)`: dispatches and waits (bounded), then
  returns the reply text and the deliverables. Long runs return a handle
  instead.
- `room_status(handle?)`: state of the dispatched work.
- `read_room(since?)`: recent messages.
- `save_component` / `publish_component`: section 5.

**Authority:** the agent never gets the bridge's session.
- `ask_agent` calls `POST /internal/v1/rooms/{room}/dispatch {parentJobId, recipient, text, expert}`.
- Realtime checks that the parent job is running and leased by this
  bridge's owner. It then posts through `PostChat` with **Author = the parent
  job's human requester** and a `dispatchedBy {jobId, depth}` marker. The
  scheduler already posts on behalf of a member this way.
- The job carries that requester, so the target bridge applies borrow
  authorization, reservation, quota, budgets, sandbox policy and the platform
  default model unchanged. There is no new authorization model.

**Guards:**
- maximum depth (default 3) and fan-out per parent job (default 8);
- no self-dispatch and no cycles;
- the requester's chat rate limit;
- per-dispatch timeouts.

Every dispatch is a visible room message ("maya → (via reviewer) →
jack/codex"). Replies and deliverables come from the existing job status,
history, and deliverables by the request's `sourceSeq`.

## 8. Expert teams

A team is an expert with a `team` block. The lead's instructions are the SOP
(phases, delegation, merging, stop rules), plus a roster of member experts
with default agents (a platform agent, the requester's own agent, or a named
member's agent). The lead dispatches with `ask_agent`.

Built in:
- a capability check against the roster before dispatching;
- a progress line per phase;
- member timeout notices, and abort after N failures;
- a pre-flight cost estimate from recent run costs per agent.

Members can run on different vendors and machines. Every hop is visible,
audited, charged and previewed. Dispatches are durable jobs and survive
restarts.

## Delivery plan

1. **Framework + skill packs + experts:**
   - content tables, the shared validator and visibility function, the
     Library;
   - `agentProfile` with the prompt-prefix fallback;
   - config-path writes and their exclusions;
   - the bnbloc-acp profile mapping;
   - personal scope and import.
2. **Room tools:** the bridge MCP server, the dispatch route with guards,
   attribution in the feed.
3. **Prompt packs:** the cutover of the prompt library, variables, slash
   commands.
4. **Sharing and approval:** shared copies, org approval, platform
   publishing.
5. **Builder:** authoring by conversation for all three content kinds.
6. **MCP tool components and expert teams.**

1 and 2 can run in parallel. 3 is independent of 2.

## Verification

- **One expert on every core.** The same expert on Claude Code, Codex,
  OpenCode and a fallback core: a marker instruction and a marker skill show
  in the answers, and the expert's MCP tool is called.
- **Skill pack at room level.** A room-enabled skill pack is used by two
  different agents in the room.
- **Prompt pack.** A slash command with a variable fills and sends. The old
  prompt library and its table are gone.
- **Dispatch.** A dispatch to a teammate's agent is refused without a grant
  and charged with one; the depth and fan-out limits hold.
- **Team.** A team of three experts on two vendors completes a task end to
  end, with every hop visible and audited.
- **Builder.** A member creates, tries, saves and publishes an expert, a
  skill pack and a prompt pack by conversation. Org scope waits for
  approval, and someone else's script-bearing skill forces a sandboxed run.

## Evidence (2026-10-12)

- **Core Protocol:** has no fields for instructions, MCP or skills
  (`core/protocol/types.go`).
- **Pre-session file writes:** allowed only under `.room/` (`core/provider.go`
  `WorkspaceFileWriter`, enforced by bnbloc-acp). A root
  `CLAUDE.md`/`AGENTS.md` would currently publish as a deliverable
  (`services/bridge/artifacts.go`); hence the exclusion list.
- **ACP:** has no standard system-prompt field (an RFD is open, and `_meta`
  is the recommended route). `session/new` takes `mcpServers`; http needs
  `mcpCapabilities.http`.
- **Experiments in temp workspaces:**
  - OpenCode read `AGENTS.md`, used a project skill and called a project MCP
    tool.
  - Claude Code read `CLAUDE.md` (not `AGENTS.md`) and used
    `.claude/skills`. It called an MCP tool end to end with `--mcp-config` +
    `--allowedTools`.
  - Codex's rendered prompt carried `AGENTS.md`, `.agents/skills` and
    `developer_instructions`, and `-c mcp_servers` spawned the server. There
    was no model answer because the test key was out of budget.
- **Jobs:** jobs are created only by `PostChat` (human chat, schedules). It
  sets the job's requester to the author, and the target bridge derives
  borrow, quota and sandbox handling from it, so dispatch inherits them.
  Agent replies never create jobs, so loops need the guards above once
  dispatch exists.
- **Sandbox networking:** the microVM guest's egress is public-only, but the
  agent CLI and its MCP servers run on the host, so a loopback MCP server
  works for sandboxed drives.
- **Components today:** signing is offline Ed25519 via the CLI. Records live
  in `platform_settings` (one TEXT row each, scanned on list), and
  install/rollout/tenants are super-admin only. That fits the executable
  kinds and the platform tier, not thousands of member-authored packages,
  hence the content tables and the shared visibility function.
- **Prompt library:** the existing `prompts` table and the
  `/v1/prompts` routes (realtime `team.go`) overlap with prompt packs, hence
  the cutover.

## Status: step 1 (framework, experts, skill packs)

**2026-10-12: implemented** (architecture:
`docs/architecture/content-components.md`; the protocol in
`docs/architecture/core-providers.md`). Commits: 34ab597 (`agentProfile`,
the agent-config path contract, the bridge session contributor hook),
c4c0c7c (manifest profile vars, conformance), 1d7ea85 (per-target MCP
transports), d7487b4 + 2fbc527 (shared semver), f3a6a76 (`contentpkg`
validator/builder/importer), 55c06fb (content tables), 1b39dd0 (one
visibility function), e6233e7 (shared live-lease rule), 1a67c55 (chat and
jobs carry the expert), fe0574c (gateway routes), 9302fab (bridge
materialization, fallback, exclusions, forced sandbox); web: dcf75a0
(Library Experts and Skills tabs: list, view, form create, import, new
version, delete, "on for all my agents"), e85aba0 (composer expert picker
beside the agent picker, `@expert` mentions, welcome and quick prompts,
first-use permission confirmation, the expert on messages and run rows),
d86eb64 (room settings: pinned experts and room skill packs). bnbloc-acp:
e4643e7 (profile mapping per agent), 38f282a (`mcpTransports`). Decisions
made while building:

- **Ownership.** The gateway serves the content routes itself: it owns the
  component surface (`/v1/components`, opt-in installs) and the artifact
  entitlement every download goes through, holds the store, and stages
  bundles in realtime's blob store like sidecar and previewer artifacts.
- **`/v1/components`** lists content components in a separate
  `contentComponents` array through the same visibility function; the
  desktop's executable catalog decodes `components` strictly and nothing of
  a content component is desktop-supervised.
- **Room uses grant use in the room.** A component pinned (expert) or
  enabled (skill pack) in a room by its owner/admins is usable by that
  room's turns and readable by its members (`?room=`), pinned to a version or
  following the latest. This is the room-level enablement of step 1; named
  shared copies stay step 4.
- **Member skill packs** apply to the agent OWNER's agents (their machine,
  their setup), also when borrowed; the chat's expert is resolved as the
  requester.
- **The expert's version is pinned when the chat is posted** (requested,
  else the room's pin, else the latest) and recorded on the job, the message
  and `extension_audit` (kind `content`).
- **A profile is per session.** Its content checksum (instructions + skill
  files; MCP servers and env excluded so per-session credentials do not
  churn sessions) is part of the bridge's session binding: different content
  starts a new session; a reused sandbox workdir has its agent-config paths
  cleared first. The fallback prefix therefore goes on a session's first
  prompt only.
- **Delete** removes a personal component, its versions, uses and acks (the
  blob collection reclaims bundles). Retire/unpublish for shared scopes is
  step 4 (the status column carries it).
- **MCP.** Content cannot declare MCP servers yet: `requires.mcp` names MCP
  tool components, none exists, so every one is refused ("reject unknown
  stdio MCP"). Session contributors (room tools) attach MCP servers today.
- **`.opencode/**`** is an agent-config path, so OpenCode's own plugin
  `node_modules` there are never published or synced either.

**Verified end to end** (local services on MySQL 8.4, a bridge driving
bnbloc-acp → OpenCode 1.18.35 with `opencode/big-pickle`): an expert with a
marker instruction (`MARKER-ALPHA`) and an embedded marker skill
(`MARKER-BETA`) picked in the chat answered "The secret phrase is
MARKER-BETA. MARKER-ALPHA"; the run record read "Marker expert v1.0.0 on
maya/acp/opencode" and the audit row was written. A skill pack imported from
a zipped skill directory and enabled for the member answered `MARKER-GAMMA`
on the next turn, in a new session (content changed).

**Open in step 1:** an optional room expert applied to every agent turn
(only pinned experts are offered in the composer); usage counts beyond
`uses` (runs/members per component); editing requirements, tags and icons in
the Library form (they carry over unchanged); the live job-status line does
not show `contentMissing` yet; org/platform scopes (step 4). All but the
scopes were closed in step 6 (see "Status: step 6 (expert teams) and the
step-1 gaps").

## Status: step 2 (room tools)

**2026-10-12: implemented** (architecture: `docs/architecture/room-tools.md`).
Commits: 71d47fe (store: dispatch parent and depth), e47947c (realtime
dispatch route and guards; 462fc62 named 5xx), 6e95c2a (bridge room tools MCP server, wired
through the step-1 session contributor hook), 6f1bb5b (gateway door),
2e3711d (feed, runs and audit attribution). Scope, as built:

- **Dispatch route.** Realtime serves `POST /internal/v1/rooms/{room}/dispatch`
  (svcauth `RequireSigned`, routed to the room's owner replica by
  `RouteKey`). The gateway's public door is `POST /v1/rooms/{room}/dispatch`:
  the ordinary proxy checks (room scope, `drive_agent`, the per-subject chat
  rate limit) run, then the request is rewritten to the internal path and
  signed with the caller's identity (the bridge's owner).
- **Guards** (protocol safety constants, not environment data): depth at
  most 3, at most 8 dispatches per parent job (counted from persisted jobs),
  no self-dispatch, no cycle along the parent chain, parent job running and
  leased by a bridge of the caller, requester still a human member, never in
  the people chat.
- **Attribution.** The post goes through `PostChat` with the parent job's
  requester as author and a `dispatchedBy {jobId, depth, via}` marker in the
  message payload; the new job persists `dispatchedByJobId` and
  `dispatchDepth` (new `agent_jobs` columns). Run records and the audit log
  (`extension_audit` kind `agent_dispatch`) show it; the feed renders
  "maya → via jack/codex → bob/claude".
- **Bridge MCP server** (`services/bridge/roomtools`): loopback, random
  port, one bearer per driven core session (constant-time compared, revoked
  when the session is dropped), bound to the job the session is currently
  driving. Streamable HTTP, dual-era (`initialize` legacy and per-request
  `_meta` modern), plus a stdio shim (`bnbloc-bridge roomtools-stdio`).
  Tools: `list_room_agents`, `ask_agent`, `room_status`, `read_room`.
- **Attached** to every session created on an `agentProfile` core: http
  with a bearer header where the target advertises http MCP
  (`Target.MCPTransports`), the stdio shim otherwise. Cores without
  `agentProfile` get no room tools (they cannot attach MCP servers).
- **Rate limit:** the gateway charges the dispatch to the calling member's
  chat bucket (the bridge owner, who is the requester for their own agent;
  for a borrowed agent the owner's bridge pays). Fan-out and depth bound
  what one request can spend.
- **Experts:** a dispatch's `expert` (`{id, version?}`; `ask_agent` takes
  `id[@version]`) goes through `ChatPost.Expert` like a typed chat's, so it
  resolves as the requester's (their own or one pinned in the room), is
  pinned on the dispatched job and applied by the target bridge; an expert
  the requester may not use is refused (`expert_not_available`).

## Status: step 3 (prompt packs)

**2026-10-12: implemented** (architecture:
`docs/architecture/content-components.md` "Prompt packs"). Scope: the
prompt pack kind, its use in the composer, per-prompt usage counts, the
Library's Prompts tab, and the clean cutover of the old prompt library.
Commits: ab8e15b (this design), 07ac053 (`contentpkg`: the kind, its
validator, form and import, the quick prompt `ref`, the `KindSpec` import
hook and `MemberUse`), 8228c02 (store and realtime: the prompt library
dropped, `content_prompt_uses`), e41473a (gateway: "Save as prompt", use
counts, prompt fields and quick prompt resolution); web: f4c0c6a (the
Library's Prompts tab, "Save as prompt" and from selected messages, room
settings, the cutover), 134a762 (composer: picker, /commands, the
variables form, expert quick prompts). Decisions:

- **A kind, not a library.** `bnbloc.prompt.package/v1` registers a
  `contentpkg.KindSpec` (its own `tarbundle` limit profile: `component.json`
  and an optional `README.md`, 1 MiB). The prompts live in `component.json`
  (`prompts: [{id, title, body, variables, tags, slash?}]`), so a listing
  reads them from the stored manifest without fetching bundles. The
  validator checks unique ids and slash commands, that every `{{name}}` in a
  body is a declared variable and every declared variable is used, a
  variable's default is one of its options, and the text and count bounds.
- **Import** (the same `contentpkg.Import`, through a kind hook instead of a
  branch): a markdown file (one prompt per heading) or a json file (a list
  of prompts, or `{prompts: [...]}`), alone or in an archive. Placeholders
  in imported bodies are declared as variables. "From selected messages"
  is the prompt pack form prefilled with one prompt per message picked in
  the room's "Save as prompt" dialog (into a new pack or one of the
  member's).
- **Scopes and uses are the content ones.** A prompt pack is personal now
  (shared, org and platform with step 4). A member turns a pack on for
  themselves (`content-uses` member scope, now open to every kind that
  declares member use: skill and prompt packs) or a room's owner/admins turn
  it on for the room. The composer offers the prompts of the member's
  enabled packs and the room's.
- **The member's personal pack** is a normal pack they own, `my-prompts`
  ("My prompts"): "Save as prompt" on a message
  (`POST /v1/content-components/prompts`) appends the prompt as a new
  version, creating the pack and turning it on for the member on first use.
- **Usage counts** are a small table, `content_prompt_uses (component_id,
  prompt_id, use_count, last_used_at_ms)`, bumped once per insertion
  (`POST /v1/content-components/{id}/prompts/{promptId}/use`), never per
  keystroke, and deleted with the component.
- **Expert quick prompts** may name a pack prompt instead of text
  (`{title?, ref: "pack/prompt-id"}`); the pack must be in the expert's
  `requires.prompts`. Views resolve it, as the viewer and in the room when
  one is given, to the highest satisfying version of a pack they may use.
- **Cutover (start empty):** the `prompts` table is dropped by a new
  migration (the locked migration that created it becomes a no-op for a
  retired table), and the realtime `/v1/prompts*` and
  `/v1/tenant/prompts/*` routes, `internal/store/prompts.go`, the prompt
  hits of global search, the Plaza's shared prompts and the web prompt
  dialogs are removed.
- **Slash commands start a message.** The `/word` menu opens only while
  the draft up to the caret is `/word`, so a path or a slash inside text
  never captures Enter.

**Verified** by the validator, store (memory and MySQL 8.4, including the
upgrade path: an existing `prompts` table is dropped), gateway route and
web helper tests (`scripts/ci.sh go` with MySQL, `scripts/ci.sh web`).
Not yet verified in a browser against running services.

**Open in step 3:** "Create with AI" (the builder) for prompt packs (step
5's builder covers experts and skill packs); starter prompts in a room
from a pack; org-shared packs on the Plaza and in global search (the
former library's shared prompts and search hits are gone until step 4's
org scope); editing a quick prompt's `ref` in the expert form (it is kept,
not edited).

## Status: step 5 (builder, experts and skill packs)

**2026-10-12: implemented for experts and skill packs** (architecture:
`docs/architecture/content-components.md` "Built-in content" and "Requests
on behalf of a job's requester", `docs/architecture/room-tools.md`).
Commits: e6b2dd8 (this design), a105077 (built-in content source and the
Expert builder), 7cd5875 (gateway: saves and reads on behalf of a running
job's requester), 5a1e9dd (bridge: `save_component`, `get_component`,
`list_components`, `publish_component`), 385aeab (report the assigned
version); web: 3ba8ac8 (Create with AI, the workshop room, Improve with
AI, built-in items read-only), 467d8b8 (save toasts with "Open in Library"
and "Try it now"), f64479d ("Save as expert" / "Save as skill pack" on a
turn's deliverables).

**Verified end to end** (local services on MySQL 8.4, a bridge driving
bnbloc-acp → OpenCode with `opencode/big-pickle`, through the routes the
web calls): in a private "Expert workshop" room pinning the builder, "help
me create a release-notes writer expert …" made the builder draft
`drafts/release-notes-writer/` (manifest, instructions, two embedded
skills, README), explain it and offer a try; "looks good, save it" made it
call `save_component`, which saved v1.0.0 into the member's Library (audit:
"saved by agent maya/acp/opencode for maya"). The next turn, wearing the
new expert, turned five merged PRs into New / Improved / Fixed notes and
left out the two internal ones. "Improve … (id cc_…)" made the builder
call `get_component` and save a new version through `save_component(id)`.
The drafted deliverables, packed and posted to the version route as the
member (the "Save as" path), validated and saved as well. The run showed
the builder reporting its draft's version instead of the assigned one,
fixed in 385aeab.

**Open in step 5:** the builder for prompt packs (after step 3's kind
lands); "it tries the draft by dispatching to an agent wearing it" (the
builder answers a sample task in the draft's role instead: an unsaved
draft cannot be worn); org/platform publishing (step 4); a pushed "saved"
event (the web learns of a save by re-listing the Library after each agent
reply in the workshop); the workshop's web flows were not exercised in a
real browser.

Scope of this pass: the builder for
experts and skill packs (prompt packs follow step 3), "Create with AI" and
"Improve with AI" in the Library, and two save paths that work on any
agent. Decisions:

- **Built-in content is a content source, not a second library.** The
  builder ships inside the binaries (`internal/contentbuiltin`, embedded
  package directories validated by `internal/contentpkg` at startup) and
  is overlaid on the content store the gateway and realtime already use,
  as components of scope `builtin`. `extpipe.MemberVisibility` admits
  built-in content to every member (read-only, never owned); the Library,
  the composer's expert picker, chat pinning, `contentuse.Effective` and
  the bundle entitlement all see it through the same calls. Its bundle is
  served from the binary under a `builtin/<sha256>` artifact key, so the
  blob store and its collection never hold it. It is versioned with the
  release: changing its files means bumping its `component.json` version.
- **One builder for both kinds** ("Expert builder", `expert-builder`): the
  interview, draft layout and save are the same; the member's request says
  which kind.
- **Save with room tools** (cores advertising `agentProfile`):
  `save_component(dir, id?)` reads `drafts/<name>/` from the session
  workspace through the core's session-files seam, packs it, validates it
  with `contentpkg` (errors go back to the agent) and posts it to the
  ordinary content routes with a `?job=&room=` grant. The gateway admits
  the grant only while the job is live and leased by a bridge of the
  caller (`store.JobLeasedBy`, the rule dispatch uses) and the job's
  requester is still a human member of the room; the component is then
  saved **as the requester** (owner = requester, personal scope). When the
  agent is not the requester's own (a borrowed or platform agent), a
  package declaring `scripts` is refused: someone else's machine may not
  plant executable content into a member's own library, where it would run
  unsandboxed; the member saves it explicitly from the deliverables
  instead. `get_component(id)` reads a component as the requester the same
  way (the "Improve" flow). `publish_component(id, scope)` keeps personal
  scope only until step 4.
- **Save without room tools** (any core): the draft files are ordinary
  deliverables; the turn's deliverables offer "Save as expert" / "Save as
  skill pack" when they hold a content `component.json`. The web packs the
  files and calls the existing create/new-version routes as the clicking
  member; the validator's message shows inline.
- **The workshop room.** "Create with AI" opens the member's private
  "Expert workshop" room (created on first use, reused later), with the
  builder pinned and picked in the composer and the member's own agent
  (else a platform agent) selected. Drafts, tries and history stay out of
  shared rooms.

## Status: step 6 (expert teams) and the step-1 gaps

**2026-10-12: implemented.** Scope of this pass: expert teams (section
8) on the room tools, the expert a dispatch asks for, and the gaps step 1
left open. MCP tool components (the other half of step 6) are not in it.
Architecture: `docs/architecture/content-components.md` ("Expert teams",
"Room expert", "Usage counts") and `docs/architecture/room-tools.md`
("Teams"). Commits: 94ea5f1 (this record), 1e00033 (the team block),
83e1008 (usage counts, the room-expert use), 747492e (team and room
expert resolution, `internal/expertteam`), 6f4e82c (activity's team
plan), 42a1943 (the role on a dispatch), f2ddcbd (gateway: usage, rosters,
room expert, `content_uses_changed`), 9cbdd5a (bridge: lead a team, wear
the room expert, the tool-layer guards, `post_progress`), c510481 (the
estimate counts each agent's runs once); web: a831453 (Library), 2439018
(composer badge and estimate), 0c4dde9 (feed role labels and notices).
The web half of the room expert, the live refresh and the job line
(`contentUsesEvents`, room settings, `AgentJobStatus`) landed inside
7a712e1: a concurrent commit in the shared working tree picked up those
staged files under its own message; the content is as intended.

**Verified end to end** (local services on MySQL 8.4, one bridge driving
bnbloc-acp → OpenCode 1.18.35 with `opencode/big-pickle`, two agent
entries `opencode` and `opencode-b` for one member, since one real agent
was available): a "Haiku team" (SOP: phase 1 writer, phase 2 critic, then
merge) with roles `writer` (requester's own agent) and `critic`
(`member:maya/opencode-b`), each wearing its own expert. The team plan
staffed both roles on `maya/opencode-b` (the lead's own agent excluded).
Asked "autumn rain on a tin roof", the lead posted "Phase 1/2: haiku
dispatch to writer" as a notice, dispatched to `writer` (feed marker
`role: writer`, expert Haiku writer, depth 1; the reply ended with the
writer expert's `WRITER-MARK`), posted "Phase 2/2", dispatched the exact
haiku to `critic` (its reply ended with `CRITIC-MARK`, a new session since
the content changed), and answered with the haiku, "Critic's score: 9/10"
and `TEAM-DONE`, in about 90 seconds. Run records show each expert and the
dispatch origin; `extension_audit` has a `content`/`use` row per job and an
`agent_dispatch` row per role dispatch. Afterwards the plan estimated $0
per request (the free model reports a zero cost), first counting the
shared agent's runs twice; c510481 fixed that. A room expert ("end every reply with
HOUSE-MARK") picked in room settings was worn by a plain chat without an
expert, and the SSE stream delivered `content_uses_changed` for the pin and
the room expert.

Decisions:

- **A dispatch's expert is already worn** (afa20f8): `ask_agent`'s
  `expert` goes through `ChatPost.Expert`, resolves as the requester, is
  pinned on the dispatched job and applied by the target bridge. A team
  role brings its expert the same way.
- **The team block.** `team: {lead?, members: [{role, expert,
  defaultAgent, description?}]}` on an expert. `lead` is the lead's role
  title (display only, default "lead"); the lead is whichever agent the
  requester addresses with the team picked. `role` is a name
  (`[a-z0-9-]`), unique, not the lead's. `expert` is `name@range`.
  `defaultAgent` is `requester` (the requester's own agent), `platform`
  (a platform agent added to the room) or `member:<id>` /
  `member:<id>/<provider>` (that member's agent). 1 to 8 members (8 is
  `wire.MaxDispatchFanOut`: a lead cannot dispatch more per request). The
  team expert's `INSTRUCTIONS.md` is the SOP. A member expert that is itself
  a team is refused when the team resolves (no nesting).
- **Two resolutions, each in one place.** Member experts resolve as the
  requester in `contentuse` (highest version satisfying the range among the
  experts they may use and the room's pinned ones, the rule skill
  requirements already follow); `GET …/effective` returns them as
  `team`. Member agents resolve in `internal/expertteam` (pure) against the
  room's addressable agents, which activity already computes for the agent
  catalogs (members' live agents plus the room's published platform
  agents): `requester` excludes the lead's own agent (a self-dispatch is
  refused), several candidates pick the first by core and provider.
  Activity serves it as `POST /v1/rooms/{room}/team-plan`, used by the
  composer (as the caller) and by the lead's bridge (`?job=`, admitted by
  `store.JobLeasedBy`, resolved as the job's requester).
- **Run time.** The lead wears the team expert. Its instructions are the
  SOP plus a generated section: the roster (role → expert → agent handle,
  online or why not), the capability rule (refuse what no role covers;
  never improvise a role) and the progress and failure rules. The roster is
  part of the session's content checksum, so a changed roster starts a new
  session. The turn binds the resolved roster to the room tools session:
  `ask_agent(role, task)` dispatches to the role's agent wearing the role's
  expert and marks the dispatch with the role (`dispatchedBy.role`); an
  unknown role or an unstaffed one is a tool error naming the roles. A team
  on a core without room tools is refused with a notice (it cannot
  dispatch).
- **Guards in the tool layer:**
  - `post_progress(text)` posts a progress line as a room notice of the
    job (its own activity session `job-<id>-progress`, so it never counts
    as the agent's reply), at most 20 per job, 500 characters each;
  - a member task still running when `ask_agent` stops waiting posts a
    notice ("reviewer (jack/codex) has not finished after 10 minutes");
  - after 3 failed member tasks of one request (refused, failed, timed
    out or cancelled) `ask_agent` refuses further dispatches with a
    message telling the lead to stop and report. 3 is a protocol safety
    constant beside the depth and fan-out limits.
- **Pre-flight cost estimate.** The team plan carries, per agent, the
  average reported cost of its last 20 finished runs in the organization
  (run records' reported usage; runs without a cost do not count) and a
  total: the lead plus each staffed role once. The composer shows it as a
  confirmation when a team expert is picked: "about $0.42 per request,
  based on the last 37 runs"; "cost unknown" for an agent without data;
  never a guess.
- **Room expert.** A room's owner or admins may pick one of the room's
  pinned experts (not a team) as the room expert, stored as a content use
  of scope `room-expert` (no schema change). Every agent turn in the room
  wears it, under the chat's own expert when there is one (the room
  expert's instructions first). It is resolved at turn time like room
  skill packs, so it is not pinned on the job; unpinning the expert clears
  it.
- **Usage counts.** Experts: runs (jobs that wore it) and members
  (distinct requesters), counted from `agent_jobs` over a new
  `(tenant_id, expert_id, requester_user_id)` index and capped at
  `store.MaxUsageCount` per component like unread badges. Skill packs:
  members who turned the pack on for themselves. Both show on Library
  cards.
- **Library form** edits requirements (skill packs and prompt packs by
  `name@range`, declared permissions), tags, icon and a team's roster.
- **Live refresh.** A content use change publishes a transient
  `content_uses_changed` event to the room (the gateway through realtime's
  internal publish); the composer and room settings refetch the room's
  uses on it.
- **`contentMissing`** (requirements no usable package satisfies, from
  the bridge's job status) shows on the live job line.
- **Left to others:** the desktop, and confining a borrowed drive on Linux
  without a sandbox (bubblewrap): someone else's script-bearing content is
  refused there until it exists.
- **Not done in this pass:** the feed labels a team's dispatches with their
  role but does not nest them under the lead's turn; the room expert is
  resolved at turn time (like room skill packs), so the job and run record
  name only the chat's expert (the running job line shows both); a team's
  member experts resolve among the requester's tenant components and the
  room's pins, so a built-in expert is not yet a team member; a composer
  plan for a platform lead names the agent, not the host routing picks at
  send.

## Status: step 6 (MCP tool components)

**2026-10-12: implemented** (architecture:
`docs/architecture/content-components.md`, "MCP tool components").
Commits: c86dd0f (this record), 2335ac8 (the kind in `pkgmeta`), 08834cc
(`internal/mcptool`: installer, per-job resolution, sealed org connectors;
`internal/sealbox`), 7a712e1 (references must name an MCP component),
b413f50 (gateway: install, catalog, save check, effective resolution,
connector routes, `BNBLOC_SECRET_STORE_KEY` on every deploy surface),
6f3b4a1 (bridge: session MCP servers, missing, forced sandbox, binding),
a017f0b (desktop: materialization through the shared verified download,
Settings → Tools commands), f84de09 (`bnbloc cores publish-mcp`), 1530471
(gateway views: the platform listing's MCP block, `requires.mcp` with env
names, super-admin connector scope), 6b25165 (web: Library → Tools,
Settings → Tools, Organization → Tool connectors, the console). Scope: the executable kind
`bnbloc.mcp.package/v1` (section 6), `requires.mcp` resolution at use
time, member and org credentials, the desktop's stdio materialization and
its Settings → Tools, the Library's Tools tab, org connectors and
`bnbloc cores publish-mcp`. Decisions:

- **An executable kind, the existing pipeline.** Platform-signed, installed
  by a super admin through `POST /v1/platform/components` (rollout, tenants
  and opt-in unchanged), contract `bnbloc.mcp.contract/v1`. The payload is
  `mcp: {tools: [{name, description}], env: [{name, description, secret,
  required}], credentials: member|org, stdio?: {args}, http?: {auth}}` and
  one of two runtimes:
  - `stdio`: per-platform `artifacts` staged through
    `POST /v1/platform/artifacts` and pinned by sha256, exactly like sidecar
    binaries. The artifact is the command; `mcp.stdio.args` are its
    arguments. No endpoint, no network permission.
  - `endpoint`: `endpoint.baseUrl` is the MCP (streamable HTTP) URL, held to
    `permissions.network` and probed through `internal/endpointpkg`. MCP's
    own discover is `initialize`, so the kind's probe posts it and accepts a
    JSON-RPC answer or 401/403 (the endpoint wants the credentials it
    declares; the platform holds none). `mcp.http.auth` is `none`,
    `bearer` (`Authorization: Bearer <env>`) or `header` (`<header>: <env>`),
    the value read from one declared env.
  Nothing runs server-side: the gateway owns the record and the member
  catalog (`GET /v1/components`, kind `mcp`), activation registers nothing.
- **Credentials.** A component declares env names (secret or not,
  required or not) and where their values come from:
  - `member`: entered on the member's desktop (Settings → Tools), stored
    0600 under the app config dir like plugin `localSettings`, read by the
    member's own bridge, never sent to the gateway. They are the member's
    own: used only for drives the member asked for themselves. A borrowed
    or platform drive does not lend the agent owner's credentials (the tool
    shows missing), so a borrower never acts with someone else's account.
  - `org`: an org admin configures one connector per (organization,
    component) (`/v1/tenant/mcp-connectors`, `manage_org`). Values are
    sealed with AES-256-GCM under `BNBLOC_SECRET_STORE_KEY` (gateway only;
    the settings key is the additional data, so a sealed row cannot be
    moved to another organization) and stored in platform settings. They
    are opened only into the effective resolution of a job in that
    organization, answered to the bridge holding the job's lease. Without
    the key org connectors are off (409), never stored in clear. No existing
    seam encrypted at rest (notifier secrets are plain platform settings),
    so `internal/sealbox` is new and generic.
- **References.** `requires.mcp: [{name, env?}]` names an MCP component;
  `env` lists the component's env names the content needs set (required
  ones are always needed). On save the gateway checks that the name is an
  MCP component visible to the author, that the env names are declared, and
  that the content declares the matching permission (`mcp.stdio`, or
  `mcp.http:<host>` of its URL), so cards show what really runs. Reading or
  materializing stored content checks syntax only: a component uninstalled
  later never makes stored content invalid.
- **Use time.** The effective resolution of a job lists each required MCP
  component with what the bridge needs (transport, the platform's sha256
  pins, args, URL and auth, declared env, org values when configured) and
  reports unknown, invisible or unconfigured ones in `missing`
  (`contentMissing` on the job line); the turn still runs. Visibility is the
  requester's, through `extpipe.MemberVisibility`.
- **Bridge.** It builds the MCP servers of the session profile: a stdio
  server from the desktop's materialized binary (`BNBLOC_MCP_TOOLS_DIR`,
  its `index.json` entry must carry the gateway's sha256 pin for this
  platform) with the env values as process env; an http server with its
  URL and auth header. Missing binaries, member credentials (from
  `BNBLOC_MCP_SETTINGS_DIR`), a core without `agentProfile` or a target
  without http MCP show as missing. A stdio tool required by content that is
  not the agent owner's own forces the sandboxed drive (with or without the
  declared `mcp.stdio`). The session binding covers the tool set by name,
  version and transport (never values); secrets stay out of logs, the
  checksum and job data.
- **Desktop.** MCP components are not supervised: the sidecar manager
  materializes each installed stdio tool's binary for this platform through
  the shared verified download (`fetch_verified`, same cache naming and GC)
  into the tools dir, writes `index.json`, and removes binaries (and member
  settings) of components that leave the catalog. The agent spawns the
  binary per session.
- **CLI.** `bnbloc cores publish-mcp --package <mcp.json> [--binary
  <platform>=<path>...]` uploads the binaries, pins them, signs and installs
  through the shared publish path.

**Verified end to end** (local services on MySQL 8.4, a bridge driving
bnbloc-acp → OpenCode 1.18.35 with `opencode/big-pickle`): a tiny Go stdio
MCP server exposing `get_codeword` (its answer is a hash of
`CODEWORD_SALT`, a member credential) was published with
`bnbloc cores publish-mcp --binary darwin-arm64=…`; the member catalog
listed it with its pin; the binary was downloaded as the member from
`/v1/artifacts/sha256/…` into the tools dir with `index.json` (the
desktop's materialization, covered by the desktop tests) and the salt was
stored in the member settings dir. An expert requiring
`codeword` was refused without `mcp.stdio` and saved with it. Asked "What
is the codeword?" with the expert picked, the agent spawned
`mcp-codeword-<sha256> --stdio` from the tools dir and answered
`CW-b5f2ae80e6`, the hash of the salt it could only get from the tool.
With the member settings removed, the next turn reported
`contentMissing: MCP tool codeword (set CODEWORD_SALT in Settings → Tools)`,
started a new session (the tool set changed) and still ran.

**Open:** the Library form does not edit `requires.mcp` (it carries the
entries over unchanged; packages and imports set them); an http tool's
OAuth-style per-member sign-in is not modelled (a member's own token in
Settings → Tools is); the desktop materializes on each catalog refresh
(60 s), so a just-published tool is missing for up to a minute.

## Status: step 4 (sharing and approval)

**2026-10-12: implemented** (architecture:
`docs/architecture/content-components.md`, "Sharing and publishing").
Commits: 44eedb9 (this record), ceb632a (store: `source_id`/`shared_by`,
the unique key with `source_id`, status changes, `content_publish_requests`,
migration `2026_10_content_sharing`), 4516b09 (`contentuse`: the author as
the trust owner, `Visible`, prompt search), 88a7ff6 (`contentpkg`: the
signable platform package), d5fc614 (`content_shared`/`content_review`
notifications), 57eafc5 (gateway: shares, copy update, publish requests,
org review and management, platform export and install; 84864ce a later
share's notification), 2bd193e (realtime:
prompt hits in global search), 40ac890 (room tools `publish_component`,
Expert builder 1.0.2), 56a04e4 (`bnbloc content export|promote`); web:
9978785 (Library: Share, Publish to organization, copies and their
updates, the Plaza's shared prompts, prompt search hits), 8622626
(Organization → Content review, Platform content).

**Verified** by store tests (memory and MySQL 8.4, including converging a
pre-step-4 table), `contentuse`, `contentpkg`, gateway route tests
(authorization: only the author shares, publishes or edits; only org admins
of the request's organization review and decide; only a super admin exports
and installs; a tampered or unsigned package is refused), room tool, CLI and
web helper tests (`scripts/ci.sh go` with MySQL 8.4, `scripts/ci.sh web`).
Not yet verified against running services or in a browser.

**Open:** notification preferences have no switch for the two new types;
a room share reaches the room's members at that moment (people who join
later do not get a copy; a room pin is the room-wide alternative); platform
content has no tenant restriction (it reaches every organization or, when
unpublished, none); org rows of two authors may share a name (requirements
resolve to the highest satisfying version among them); the publish
endpoint does not auto-open a request when an author saves a new version
(the Library shows the version the organization uses beside the latest).

**Closed elsewhere:** step 3's open "org-shared packs on the Plaza and in
global search", and step 6's "a built-in expert is not yet a team member"
(`contentuse.Visible` includes built-in and platform content in `name@range`
resolution).

Scope: section 1's shared, organization
and platform scopes for every content kind (experts, teams, skill packs,
prompt packs), the builder's `publish_component` for them, and what the
prompt-library cutover left to this step (org-shared prompts on the Plaza
and in global search). Executable components are unchanged. Decisions:

- **A shared copy is a component row, not a grant.** Sharing creates (or
  moves forward) a component of scope `shared` owned by the recipient, with
  `source_id` (the author's component) and `shared_by` (the author); its
  versions are the author's version rows cloned (same manifest, same
  content-addressed bundle, so no bytes are copied). Why not a pinned
  read-only grant: every existing read path (`extpipe.MemberVisibility`'s
  owner rule for `shared`, `contentuse.Usable`/`Effective`, the Library
  listing, the bundle entitlement, member uses, acks) already handles an
  owned row, while a grant would add a second visibility input to all of
  them; and a grant dies with the author's component, where a copy keeps
  working when the author deletes or rewrites theirs ("later edits by the
  author don't change it"). A copy is read-only (no new versions, no
  re-sharing); the recipient may delete it or **update it to the author's
  latest** explicitly, which appends the author's current version.
- **Trust follows the author, not the row.** A copy is owned by the
  recipient but was written by someone else: the trust owner of a
  component (`ContentComponent.Author()`: `shared_by` when set, else the
  owner) is what `contentuse.Ref` reports to the bridge, so someone else's
  script-bearing or stdio-MCP content still forces the sandbox, and the
  Library still asks the recipient to confirm its permissions on first use.
- **Rooms.** Sharing with a room copies to each current human member of
  the room (the author must be one). Pinning a component in a room (step 1)
  stays the room-wide alternative for owners and admins.
- **Recipients** are members of the author's organization (at most 100 per
  share). A recipient already holding a copy of the same component gets the
  newer version appended (never an older one). The unique name key of
  `content_components` gains `source_id`, so copies of two authors'
  same-named components coexist.
- **Organization scope is a second row, approved version by version.**
  `publish_component(id, "org")` (or the Library's "Publish to
  organization") opens a request for the author's current version
  (`content_publish_requests`, the request/approve pattern of
  `shared_agent_quota_requests`: one pending request per component, a newer
  request supersedes it). An org admin (`manage_org`, own organization only;
  a super admin may act in any) reviews it in the Organization console:
  declared permissions, the files that run (scripts), MCP requirements,
  a team's roster, and a file-by-file diff from the version the
  organization uses now. Approving creates (or moves forward) the org row
  (scope `org`, owner = author, `source_id` = the author's component) with
  that version; members see it in their Library and keep using that version
  until a newer request is approved. Declining records the note. The author
  is notified either way.
- **No opt-in rollout for content.** Org and platform content is listed to
  every member it reaches (rollout auto). A second switch would duplicate
  the one members already have: they pick an expert per message and turn a
  pack on for themselves. Admins control reach by approving, unpublishing
  (status `retired`: hidden from the Library and new use; rooms that pinned
  it keep their version, as section 1 says) and removing (the row and its
  uses are deleted). An author may take their own component out of the
  organization too (`publish_component(id, "personal")`).
- **Platform scope is the existing signed install.** A super admin exports
  any content component version as a package
  (`GET /v1/platform/content-components/{id}/package`, or
  `bnbloc content export`): a `pkgmeta` envelope of the content kind whose
  one artifact is the bundle on the gateway (`/v1/artifacts/sha256/…`,
  pinned by sha256). It is signed offline and installed with the existing
  `bnbloc cores install --package … --signature …` (or `bnbloc content
  promote`, which exports, signs and installs). `POST
  /v1/platform/components` recognizes a content kind, verifies the
  signature against the trusted keys, re-validates the bundle and stores a
  platform row (no tenant, no owner) with the signature on the version.
  The console's Content page lists platform and org content and shows the
  commands; it never signs.
- **Notifications** go through the existing emitter (the gateway's
  producer): `content_shared` to each recipient, `content_review` to the
  organization's admins for a request and to the author for a decision.
  Both are room-less and link to the Library or the Organization console.
- **Audit:** `extension_audit` kind `content`, actions `share`, `update`,
  `request`, `approve`, `decline`, `unpublish`, `restore`, `remove`,
  `install` (platform).
- **Org-shared prompts are back** through org-scoped prompt packs: the
  Plaza shows the organization's prompts again, and global search returns
  prompt hits from the packs the member can see.

## Gap closure (content features)

**2026-10-12: recorded, in development.** The gaps the steps above left
open for content components, closed through the existing seams. Runtime
gaps (bridge, room tools, feed, desktop) are a separate pass. Decisions:

1. **Notification preferences.** `content_shared` and `content_review`
   get their switches in the existing prefs model: `contentShared` and
   `contentReview` in `wire.NotificationPrefs` (store columns
   `content_shared`, `content_review`, default on; migration
   `2026_10_notification_prefs_content`), `notify.Allows` reads them, and
   Settings → Notifications shows them beside the other types.
2. **Room shares reach later joiners.** A share with a room records the
   room share (`content_room_shares (room_id, component_id)`: tenant,
   version, author, note; moved forward by a later share, never back;
   deleted with the component). There was no join hook, so the seam is
   added once beside `member_removed`: every human join in the room
   service (add member, join a shared room, redeem an invite, a template's
   default members) publishes a transient `member_joined` room event
   (`{userId}`), and realtime's publish tap hands it to the content share
   step (`internal/contentshare`, the copy logic extracted from the
   gateway, which uses it too): each room share of that room gives the
   joiner a copy (the same copy rule as a share), with the
   `content_shared` notification and a `share` audit row. A room share
   stops handing out copies when its author has left the room. The author
   revokes one with `DELETE /v1/content-components/{id}/shares/rooms/{room}`:
   later joiners get nothing, existing copies stay. `GET …/shares` lists
   the room shares beside the copies.
3. **Platform content tenant scope.** A platform row carries
   `enabled_tenants` (NULL = every organization; migration
   `2026_10_content_platform_tenants`). `extpipe.GateForContent` passes it
   to `MemberVisibility`, whose platform branch already applies the
   component tenant rule, so `contentuse.Visible`, `Usable`, `name@range`
   resolution, prompt search and the bundle entitlement all honour it.
   It is set through the existing route,
   `PUT /v1/platform/components/{name}/tenants`, with its fail-closed
   semantics (an empty list is a 400, `{"allTenants": true}` clears it):
   `?kind=expert|skill|prompt` picks platform content, and without a kind a
   name held by both an executable record and platform content is a 409.
   The console's Platform content page edits it.
4. **Same-name org components: org names are unique per organization.**
   Two options: author-qualified references, or one org component per
   (organization, kind, name). Uniqueness is the cleaner one with the
   existing syntax: `name@range` stays what authors write (in
   `requires.skills`, `requires.prompts`, team roles, quick prompt
   `pack/prompt-id`, whose `/` an author prefix would collide with), so
   the validator, the resolution and every form stay as they are and
   nothing is migrated in stored content; an author-qualified syntax would
   change all of them and still leave `name@range` ambiguous for existing
   content. The store holds it: an org row reserves its name (an
   `org_name` column, set for scope org whatever the status, so an
   unpublished row can be restored; `UNIQUE (tenant_id, kind, org_name)`,
   migration `2026_10_content_org_names`, which keeps the oldest of any
   existing duplicates and retires the others). Requesting and approving
   a clashing name are refused with a clear message ("the organization
   already has a skill pack named review-kit by jack: rename yours, save
   it under a new name, and ask again"); the author renames by saving
   under a new name.
5. **Re-approval on new versions.** When the author saves a new version
   (form, archive, import, or the room tools) of a component that the
   organization uses (an active org row) or that has a pending request,
   the gateway opens the publish request for the new version itself
   (one pending request per component: it supersedes the older one; the
   org admins are notified only when no request was pending, so repeated
   saves do not repeat the notification). The save's reply carries the
   request (`resubmitted`), and the Library tells the author ("sent to
   your organization's admins for approval; members keep using v1.1.0
   until then"). An unpublished or withdrawn publication is not resubmitted.
6. **"Create with AI" for prompt packs.** The Expert builder learns the
   prompt pack format (prompts with variables, slash commands, the starter
   flag) and is bumped to 1.1.0; the Prompts tab gets "Create with AI" and
   "Improve with AI" through the same `workshopFlow`; the builder drafts
   `drafts/<name>/component.json` with prompts and variables and saves
   through `save_component`, or the member uses "Save as prompt pack" on
   the deliverables.
7. **Room starter prompts from packs: a `starter` flag on a pack prompt.**
   The pack's author marks the prompts that open a conversation well
   (`starter: true`); a room's empty-feed starter prompts are the starter
   prompts of the packs turned on for the room, then the generic examples.
   Why the flag and not a per-room pick: the room's owner already chooses
   by turning the pack on, the flag needs no second per-room list (no
   schema, no settings to keep in step when a pack changes), and the
   author knows which prompts stand alone. A starter with variables asks
   for them like any pack prompt.
8. **Form editing.** The expert form edits a quick prompt's `ref`: pick a
   prompt of a pack the member can see (the pack is added to
   `requires.prompts`). The Library form edits `requires.mcp`: pick from
   the MCP tool components visible to the member, tick the env names the
   tool declares, and the matching permission (`mcp.stdio`, or
   `mcp.http:<host>`) is added; the gateway's save check stays the
   authority.
9. **A pushed "content saved" event.** A save or publish by the room tools
   (a request on behalf of a job's requester) publishes a transient
   `content_saved` room event in the job's room (`{componentId, kind,
   name, displayName, version, action: saved|submitted, created, owner}`)
   through realtime's internal publish, like `content_uses_changed`. The
   workshop listens for it on the room stream and toasts the requester's
   saves; the re-list of the Library after every agent reply is removed.
   No new stream type.
10. **MCP console form.** The platform console builds an MCP tool package
    from a form: name, version, publisher, tools, env (secret, required),
    credentials source, and either stdio binaries per platform (uploaded
    through the existing `POST /v1/platform/artifacts` staging) with
    `args`, or an http URL with its auth and network permission. The
    package is assembled by the gateway
    (`POST /v1/platform/mcp-packages`) with `pkgmeta.AssembleMCP`, the
    assembly `bnbloc cores publish-mcp` uses (extracted from the CLI, not
    copied), and returned as the JSON to sign. Signing stays offline: the
    console offers the file and explains the CLI step (`bnbloc cores
    install --package <file>` signs with the publisher key and installs,
    or a detached signature goes with the file into Components → Publish),
    like content promote.
