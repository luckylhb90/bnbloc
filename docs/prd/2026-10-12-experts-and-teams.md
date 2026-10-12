# Content components: experts, skill packs, prompt packs; room tools and expert teams

Status: approved, in development (2026-10-12). Feasibility verified in
code and by experiments (see "Evidence").

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

## Status: step 2 (room tools)

**2026-10-12: in development.** Scope of this step, as built:

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
