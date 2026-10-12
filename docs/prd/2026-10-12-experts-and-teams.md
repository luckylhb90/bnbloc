# Experts, room tools and expert teams

Status: design, approved for planning (2026-10-12). Feasibility verified in
code and by experiments (see "Evidence").

## Why

Members want to "hire" a specialist for a task: a reviewer, a release
manager, a contract checker, a market researcher, a support agent. Today a
room offers agents (cores × providers × owners) but no way to give an agent a
role, a method, a toolset, or to let one agent bring in others.

bnbloc runs any agent: coding agents, office agents, general agents and
digital-employee agents, on members' machines, on platform hosts, or behind a
protocol adapter. So an expert must not be a feature of one agent runtime. It
is a **bnbloc-level, agent-neutral package** that each core wears the way it
can. This is what single-runtime products (an expert that only runs on their
own agent) cannot offer: one expert, any agent, any member's machine, with
borrowing, quotas, audit and sandboxing already around every run.

Three capabilities, plus authoring:

- **A. Experts:** a package with a persona and method (instructions), skills,
  required tools (MCP servers by name), quick prompts and display metadata.
  Picked explicitly; active for the whole conversation, not left to the model
  to trigger like a skill.
- **B. Room tools:** an MCP server bnbloc gives every agent session, so an
  agent can see the room's agents and **dispatch work to another agent**
  (`ask_agent`), under the human requester's authority.
- **C. Expert teams:** a lead expert with a roster and an SOP, dispatching to
  members through B.
- **D. Authoring by conversation:** members create, revise and publish
  experts by talking to a builder expert, with personal, shared, org and
  platform scopes.

## A. Experts

### Package

```
expert/
  expert.json          name, version (semver), displayName, description,
                       category, tags, avatar, quickPrompts[{title, prompt}],
                       welcome, kind: "expert" | "team",
                       requires: { mcp: [{name, transport, env: [names]}],
                                   permissions: ["scripts", "mcp.stdio", "mcp.http:<host>"] },
                       team: {lead, members[{role, expert, defaultAgent?}]}   (teams only)
  INSTRUCTIONS.md      persona and method: the always-on part
  skills/<slug>/SKILL.md (+ scripts, references)
  avatar.png
```

The layout deliberately matches the common skill format (`SKILL.md` with
frontmatter), so existing skill and plugin collections import with a manifest
added. An expert names no model and grants no permission of its own: the
model is the member's or the platform agent's choice, and tool reach stays
with the core, the borrow grant and the sandbox.

### How a core wears an expert (verified per agent)

One new optional capability, `agentProfile`, on Core Protocol v1
`CreateSession`: `{instructions, skills: [{name, files}], mcpServers (ACP
shape), env}` (secrets never logged or persisted, like `ExecToken`). Each
core maps it to its native form:

| Core form | Instructions | Skills | MCP |
|---|---|---|---|
| Claude Code (protocol daemon or ACP) | `--append-system-prompt` / ACP `_meta.systemPrompt.append`; `CLAUDE.md` read from cwd | `.claude/skills` in cwd (verified) | `--mcp-config … --strict-mcp-config` with an allow-list (verified end to end) |
| Codex (daemon or ACP) | `developer_instructions` / `AGENTS.md` (verified in the rendered prompt) | `.agents/skills` (verified) | `-c mcp_servers.*` (server spawn verified) |
| OpenCode (ACP) | `AGENTS.md` (verified) | `.opencode/skills` (verified) | project `opencode.json` (tool call verified) |
| Gemini / other ACP | `GEMINI_SYSTEM_MD` or `_meta` per agent | files where supported | ACP `mcpServers` |
| Manifest daemons | new `{instructions}` template var + raw-JSON vars | daemon-defined | daemon-defined |
| Any core without the capability | **fallback: instructions prepended to the prompt by the bridge** (works today, every core) | skills summarized into the instructions | none |

The bnbloc-acp adapter gets a per-agent mapping table in `agents.json`
(`profile: {instructionsVia: "meta" | "env:<NAME>" | "file:<path>", skillsDir,
mcp: "acp" | "flag"}`), so a new agent is configuration, not code.

Workspace files: the bridge already writes `.room/` into the session's own
workspace before `CreateSession`. The `WorkspaceFileWriter` contract widens
from `.room/` only to a fixed list of agent-config paths (`CLAUDE.md`,
`AGENTS.md`, `.claude/skills/**`, `.agents/skills/**`, `.opencode/**`,
`.mcp.json`). Those paths are excluded from deliverable publishing and code
sync, and write-denied to the agent in sandboxed runs. The workspace is a
bnbloc-owned directory per room × core × provider, never the member's repo,
so nothing of the member's is overwritten. Files are re-materialized by
checksum when the expert version changes (not copied once).

The member's own global agent configuration (their global `CLAUDE.md`,
skills, MCP) still loads alongside: an expert adds a role, it does not wipe
the member's setup.

### Picking an expert

- Composer agent picker: "Expert" + "runs on" (my agent / a borrowed agent /
  a platform agent).
- `@reviewer` in a room: resolves to the room's pinned expert, run on the
  sender's default agent unless they choose another.
- Room settings: pinned experts (listed first) and an optional room expert
  applied to every agent turn in the room.
- Quick prompts and the welcome card come from `expert.json`.
- Audit and the job row record "expert X v1.2 on agent Y of member Z".

## B. Room tools (agent dispatches agent)

### Where

A loopback MCP server inside the bridge (127.0.0.1, random port, per-session
bearer compared in constant time, revoked when the session ends), the same
pattern as the desktop plugin host API. The agent CLI runs on the host even
for sandboxed drives (only shell commands go to the microVM), so it reaches
the loopback server; the guest VM, whose egress is public-only, never needs
to. Platform agents run on a host member's bridge and get the same server.

The server is attached through `agentProfile.mcpServers` (http transport with
the bearer header, or stdio via a tiny `bnbloc-room-tools` shim for agents
without http MCP).

### Tools

- `list_room_agents()` → each agent's handle, owner, core, provider, online
  state, pinned expert and description.
- `ask_agent(handle, task, expert?)` → dispatches and waits (bounded) for the
  reply; returns the reply text and the deliverables (name, kind, link).
  Long runs return a handle instead.
- `room_status(handle?)` → the state of dispatched work.
- `read_room(since?)` → recent room messages (already materialized in
  `.room/`; the tool is a convenience).

### Authority and safety

The agent never gets the bridge's session. `ask_agent` calls a new internal
route, `POST /internal/v1/rooms/{room}/dispatch {parentJobId, recipient,
text, expert}`. Realtime checks that the parent job is running and leased by
this bridge's owner, then posts through the existing `PostChat` with
**Author = the parent job's human requester** and a `dispatchedBy {jobId,
depth}` marker. The scheduler already posts on behalf of a member this way.
The job created from it carries that requester, so the target bridge applies
borrow authorization, reservation, quota, budgets, sandbox policy and the
platform default model unchanged. No new authorization model.

Guards: maximum depth (default 3), fan-out per parent job (default 8), no
self-dispatch and no cycle in the chain, the requester's normal chat rate
limit, a per-dispatch timeout, and the parent job's own lease and timeout.
Every dispatch is a visible room message attributed "maya → (via reviewer)
→ jack/codex", so the room sees the whole chain.

Replies and deliverables are read from what exists: `GET .../jobs/{jobId}`
(status, reply sequence), the reply in history, and deliverables by the
request's `sourceSeq`.

## C. Expert teams

A team is an expert with `kind: "team"`: the lead's instructions (the SOP:
phases, what to delegate to whom, how to merge, when to stop) plus a roster
of member experts, each with a default agent ("platform agent", "the
requester's own agent", or a named member's agent). The lead runs as an
ordinary expert and uses `ask_agent` to dispatch.

Built in from the guards above:

- **Capability check:** the lead checks the request against the roster before
  dispatching and refuses what nobody covers.
- **Progress:** each phase posts a short status line to the room.
- **Timeouts and failure policy:** a member timeout notice, abort after N
  failures.
- **Cost:** a pre-flight estimate before a team runs, since one team turn
  spends several agents' worth. The estimate draws on recent run costs per
  agent from the run records.

Members can run on different vendors and different machines (a lead on one
member's agent, a reviewer on a teammate's agent, a researcher on a platform
office agent). Every hop is visible, audited, charged and previewed in the
room. Dispatches are durable jobs, so nothing is lost on a restart.

## D. Authoring by conversation

A built-in **expert builder** (a platform expert) interviews the member about
the role, method, tools and examples. It drafts the package into a visible
`expert/` directory of its workspace, so the draft shows as deliverables the
member can read. It can also try the draft: dispatch a sample task to an
agent wearing the draft. Two room tools:

- `save_expert(dir)` → the bridge reads the files (session files), packs them
  and posts them **as the requester** to `POST /v1/experts`. The server
  validates paths, sizes (a new `tarbundle` limit profile), the manifest
  schema, declared permissions and MCP references, then stores a draft version.
- `publish_expert(id, scope)` → changes visibility (below).

Editing works the same way ("make it stricter about tests"): the builder
loads the current version and saves a new one. Members can also edit in a
form in the Library.

### Scopes and trust

| Scope | Who sees it | How |
|---|---|---|
| Personal | the author | default after save |
| Shared copy | named members or a room | the recipient gets a copy; the author's later edits don't change it |
| Organization | the org | an org admin approves a pending request (the quota-request pattern under `/v1/tenant/*`, `manage_org`) |
| Platform | every org | a super admin publishes it as a signed `bnbloc.expert.package/v1` component (CLI-signed, the existing rollout and tenant scope) |

Storage: `experts` and `expert_versions` tables, modeled on prompts and room
templates. The manifest is stored in the row, the bundle in the blob store by
sha256 (added to `ReferencedBlobKeys`; `artifactEntitled` grants it to the
members who may use it). Versions are immutable; a room or a member pins a
version or follows the latest.

Risk: an expert's skill scripts and stdio MCP servers run on the machine of
whoever uses the expert. Rules:

- Declared permissions (`scripts`, `mcp.stdio`, `mcp.http:<host>`) are shown
  on the card and approved at the org or platform scope.
- An expert that is not the member's own and declares scripts or stdio MCP
  **forces a sandboxed drive** (the existing `decideSandbox` and
  `sandboxRequired` path), or is refused where no sandbox is available.
- stdio MCP servers come only from an admin-approved catalog. Experts
  reference servers by name.
- Secrets never ship. MCP entries declare required env names; values come
  from the member's local settings (as plugin `localSettings` do) or an
  org-shared connector.
- First use asks the member to confirm the declared permissions.

## Delivery plan

1. **Experts (A):**
   - the `agentProfile` capability and the prompt-prefix fallback;
   - widened config-path writes and their exclusions;
   - the per-agent mapping in bnbloc-acp;
   - `experts` storage and the Library and picker UI;
   - personal scope.
2. **Room tools (B):** the bridge MCP server, the dispatch route with guards,
   and the attribution UI.
3. **Authoring (D):** the builder expert, `save_expert` and `publish_expert`,
   and the shared, org and platform scopes with approval.
4. **Teams (C):** the team manifest, roster defaults, the cost pre-flight and
   the team card.

Steps 1 and 2 are independent and can run in parallel.

## Verification

- **Each core:** the same expert on Claude Code, Codex, OpenCode and a
  fallback core. A marker instruction and a marker skill show in the answer,
  and an MCP tool from the expert is called.
- **Dispatch:** dispatch from one member's agent to a teammate's agent goes
  through borrow authorization (refused without a grant, charged with one).
  Depth and fan-out limits hold.
- **Team:** a team of three experts on two vendors completes a task end to
  end in a room, with every hop visible and audited.
- **Builder:** a member creates, tries, saves and publishes an expert by
  conversation. The org scope waits for approval. A script-bearing expert
  from someone else forces a sandboxed run.

## Evidence (2026-10-12)

- **Core Protocol:** has no instructions, MCP or skills fields today
  (`core/protocol/types.go`). The bridge writes pre-session files only under
  `.room/` (`core/provider.go` `WorkspaceFileWriter`, enforced by
  bnbloc-acp). Root `CLAUDE.md`/`AGENTS.md` would currently publish as
  deliverables (`services/bridge/artifacts.go`), hence the exclusion list.
- **ACP:** has no standard system-prompt field (RFD open; `_meta` is the
  recommended route). `session/new` takes `mcpServers`, with http gated by
  `mcpCapabilities.http`.
- **Experiments in temp workspaces:**
  - OpenCode: read `AGENTS.md`, used a project skill, and called a project
    MCP tool.
  - Claude Code: read `CLAUDE.md` (not `AGENTS.md`) and used
    `.claude/skills`. With `--mcp-config` plus `--allowedTools` it called an
    MCP tool end to end. A project `.mcp.json` loaded, but the tool call is
    gated by its permission mode.
  - Codex: the rendered prompt carried `AGENTS.md`, `.agents/skills` and
    `developer_instructions`, and `-c mcp_servers` spawned the server. No
    model answer was obtained: the test key was out of budget.
- **Jobs and dispatch:** jobs are created only by `PostChat` (human chat and
  schedules). `PostChat` sets the job's requester to the author, and the
  target bridge derives borrow, quota and sandbox handling from it, so
  dispatch inherits them. Agent replies never create jobs, so loops are
  impossible today and need the guards above once dispatch exists.
- **Sandbox networking:** the microVM guest's egress is public-only, but the
  agent CLI and its MCP servers run on the host, so a loopback MCP server
  works for sandboxed drives too.
