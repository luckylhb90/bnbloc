# ACP core in the agent sandbox: confined, with its commands routed into the space (2026-10-22)

Status: built 2026-10-22 and released (ACP core 0.2.x): opencode and the Claude
agent (through npx) are recognised from their command and run confined with their
shell commands routed; verified with the real helper and the installed agents
(opencode 1.18.35, the Claude ACP agent): each starts confined, answers a turn, and
a bash command it is asked to run is routed to the helper (it fails there for want
of a sandbox service) and leaves no file on the host. Codex is recognised and not
built (§2.1). Owner: the ACP core (`github.com/luckylhb90/bnbloc-acp`), with
the bridge's sandbox decision (`services/bridge/sandbox.go`) and the Sandbox
contract's host confinement (`cmd/bnbloc-vmexec`) unchanged. Supersedes the
"Sandbox integration" section of
[`2026-10-11-acp-core.md`](2026-10-11-acp-core.md).

The agent sandbox runs a member's agent CLI on the host, confined by the OS
sandbox, while the shell commands it runs execute in a Linux VM space
([`2026-10-09-agent-vm-sandbox.md`](2026-10-09-agent-vm-sandbox.md)). A core opts
in by advertising `sandboxExec`. The ACP core does not, so with sandbox mode
`all` an ACP agent runs on the host, unconfined, **without a word**: the
member's global setting did not apply to it. This record makes it apply to the
ACP agents that can honour it, and refuses the rest.

## 1. Why

- 2026-10-22. A member with sandbox mode `all` ran an agent through the ACP core
  and it ran on the host: `decideSandbox` returns "not sandboxed" for an own
  drive of a core without `sandboxExec` (no refusal, no notice), and the ACP
  core's README and design note say so ("its agents run on the host"). Another
  core's agent, in the same room, was confined, which is how the difference
  surfaced: a tool that works unconfined failed in the confined one.
- The reason given for not advertising was that an ACP agent may run shell
  commands itself rather than through client terminals, so a generic adapter
  cannot force the routing. That is true of routing into the VM. It is not true of
  confinement: the OS sandbox covers the agent process **and everything it
  spawns**, whatever way it runs a command.
- The routing seam exists per CLI, and a sibling core already ships it for
  opencode: `confine --harness opencode`, a `tool.execute.before` plugin loaded through
  `OPENCODE_CONFIG_DIR` that hands each bash call to `vmexec hook opencode`, the
  plugin directory write-denied, the exec token in a file renewed each turn. The
  spike verified it (docs/prd/2026-10-09-agent-vm-sandbox.md). The same CLI, run
  through the ACP core, simply lacks the wiring.

## 2. What it is (the end state)

### 2.1 Which agents

Nothing is configured: the core **recognises the routing from the agent's
command**, as `vmexec` already derives a harness from the executable. Today:

| Agent (command) | Routing | State |
|---|---|---|
| `opencode` | a `tool.execute.before` plugin through `OPENCODE_CONFIG_DIR` (`vmexec hook opencode`) | built |
| `@agentclientprotocol/claude-agent-acp` (also by an `npx` or another path) | Claude Code's `CLAUDE_CODE_SHELL_PREFIX` router (a link to `vmexec`) | built |
| `@agentclientprotocol/codex-acp` | Codex's `CODEX_EXEC_SERVER_URL` and a bridge to `codex exec-server` in the space | recognised, **not built** |

The earlier version of this record asked the operator to set a `sandbox` field per
agent; that made a member edit a file, and was dropped. The field remains as an
optional **override** (`opencode`, `claude`) and `off`.

An agent with no routing, or `off`, **cannot run sandboxed**: the core answers
`bad_request` ("the agent <name>: …", with why) to a create or an input that
carries an `execution`, and the drive fails visibly. A sandboxed turn never runs
its commands on the host.

Codex is not built because it needs three things this core does not have: a
WebSocket bridge to the exec server (about 190 lines and a dependency), a Linux
`codex` in the sandbox guest image, and a check that the ACP adapter honours
`CODEX_EXEC_SERVER_URL` (not verified).

The core advertises `sandboxExec` (and offers workdir workspaces) whenever the
helper `bnbloc-vmexec` can confine on this machine. The capability is the core's;
the answer per agent is the refusal above.

### 2.2 The workspace

The bridge creates a sandboxed session's workspace **in** the sandbox workdir
(`CreateWorkspaceRequest.workdir`), so the room context it writes before the
first turn lands where the confined agent and the guest see it. The core adopts
that directory as a workspace: absolute, existing, a directory. It remembers the
directory beside the workspace id and never owns it: deleting the workspace
forgets the record and removes nothing in the directory (the bridge and the
sandbox own it). A workspace made this way takes only sandboxed sessions, and
`execution.sandbox.workdir` must equal it: anything else is `bad_request`, so a
sandbox workspace is never run on the host.

### 2.3 The agent process

For a sandboxed session the core starts the agent as
`<vmexec> confine --harness <h>… [flags] -- <agent> <args>` in the workdir, with
the descriptor in its environment (`protocol.VMExecEnv`, including the exec
token **file**), the routing's environment, and `PWD` set to the workdir (a
confined process lstats `$PWD`). The harness list is the routing's
(`opencode`, plus `claude`, whose skills and state opencode reads and writes, as
the sibling core does).

- **Token file.** The exec token is short-lived and a new one arrives with each
  turn, while the agent process lives across turns. The core writes the current
  token to a file in a directory of its own (0600; the agent may read the
  directory and never write it), at session create and at every input, and
  `vmexec` reads it at each command. The process never holds a stale token.
- **npx-launched agents.** A launcher such as npx installs the agent's package
  into npm's cache, which the confinement keeps read-only (without a cache of its
  own `npx -y` fails with EPERM, checked). A confined agent gets one (`npm-<agent>`
  in the core's state: read-write to it, used by confined runs only). The
  exposure is that a prompt-injected agent could plant a package there that a
  later confined run executes: still confined, never the core's own starts. The
  first start downloads the package (about 0.5 GB for the Claude agent).
- **claude routing.** `CLAUDE_CODE_SHELL_PREFIX` is a link named for the router
  to `vmexec`, in the token directory (readable, never writable by the agent):
  Claude Code quotes a prefix without a " -" as one word and routes only an
  absolute path.
- **opencode routing.** A config directory of the core's own holds the plugin and
  is passed as `OPENCODE_CONFIG_DIR` (additive to the user's config); it is
  write-denied to the agent, so it cannot plant a plugin that undoes the routing.
  The plugin is the sibling core's, whose rewrite and host-passthrough rules live
  in `vmexec hook opencode` only. opencode will not start in a config directory it
  would still have to set up, so the directory is warmed once per opencode version
  with a confined `opencode models`. The setup is ready within seconds while that
  command may go on (it asks the network), so the directory is polled and the
  command ended when it is ready; the preparation is one at a time and not tied
  to the request, so a request that times out leaves it running and the retry
  finds it done.
- **Client terminals are off.** The core advertises the ACP `terminal` client
  capability to agents today and runs those commands itself, on the host. In a
  sandboxed session it advertises none, so an agent uses its own tools, which are
  confined and routed. (Routing the core's own terminals through `vmexec run` is
  possible and is not needed for opencode.)
- **A changed descriptor.** The session pins what the spawn depends on (space,
  workdir, mounts, host confinement, passthrough, forwarded environment): every
  field but the token and its expiry. An input whose descriptor differs stops the
  agent process and continues the same ACP session in a new one, as it already
  does after an idle stop; an agent that cannot resume or load sessions refuses
  the input (`bad_request`) rather than keep running in the old scope.

### 2.4 What does not change

- A session without `execution` runs on the host exactly as today. The decision to
  sandbox stays the bridge's: it sends `execution`, the core honours it or
  refuses; the core never decides to sandbox on its own.
- The request policy (`permissions`), the profile mapping, models, turn
  deadlines: untouched. In a sandboxed session an operator may allow `execute`
  since the agent's commands are routed.

### 2.5 Host-run tools

A confined agent confines the MCP servers it spawns. An MCP tool that holds a
member's sign-in is placed on the host and fronted by the bridge
([`2026-10-22-host-run-mcp-tools.md`](2026-10-22-host-run-mcp-tools.md)); the
agent is given its endpoint, which every ACP agent can use (http, or the stdio
shim every ACP agent accepts). This record is what makes that apply to ACP agents.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member (own agent: sandboxed in mode `all`); a borrower (borrowed drives were already refused or run per tenant policy: now they can be sandboxed too, where the agent is configured); the operator (writes the agent's `sandbox`); the bridge (decides, sends `execution`); the confined agent (cannot touch the plugin directory, the token file or the routing). |
| Time and lifecycle | The token is renewed through its file at each input; the agent process outlives turns. A descriptor change continues the session in a new process. An idle stop and a restore use the descriptor the session pinned. Closing the session removes its token directory; deleting the workspace forgets it without touching the directory. A bridge restart restores sessions as for any ACP session. |
| Scale and cost | One confined process per session as today; `vmexec confine` execs the agent, so no extra process. The warm-up is once per opencode version (minutes the first time, never per session). |
| Failure and recovery | An agent that cannot be sandboxed, a missing `vmexec`, an unsupported OS (confine refuses outside macOS and Linux), a plugin directory that cannot be set up: each refuses the create or input with the reason (`bad_request` or `core_unavailable` as the cause is the request's or the machine's), and nothing runs on the host. The turn never silently loses its routing: an unreadable plugin result makes opencode refuse the command, as the plugin throws. |
| Boundaries and privacy | The token never reaches a log or the agent's environment beyond the file path; the file's directory is read-only to the agent. The agent's reads follow the confinement profile (credentials, other projects, the desktop's data denied), plus `denyRead` for host tools' state. The agent's own state directories are the harness's. |
| Abuse and untrusted input | The agent cannot rewrite its routing (plugin directory, config, token file are write-denied; the workspace's protected paths are the bridge's list), cannot reach the VM beyond its space (the token is space-scoped), and cannot run a command around the plugin: opencode's other command paths are covered by the confinement, not the routing, and are the known limit of this seam (§5). |
| One owner | The confine invocation, the environment and the plugin text are the sibling core's today; the ACP core must not grow a second diverging copy. The plan is to move the descriptor-to-process mapping into a shared helper in `core/protocol` (the environment already is: `VMExecEnv`) and keep only per-core parts (plugin directory, state paths) in each core. |
| Defaults and fallbacks | The routing is recognised from the command (a table in the core, like vmexec's harness list); an unknown agent has none and is refused when asked to sandbox: there is no fallback to the host, in any mode. No timeouts are invented beyond the warm-up's existing bound. |
| External contracts | Core Protocol `sandboxExec` (`execution` on create and every input, `workdir` on workspace create, `executionRefresh` not needed: the token file is renewed at each input and a long turn is covered by the bridge's renewal, see §5); `bnbloc-vmexec confine` and `hook opencode`; opencode's `OPENCODE_CONFIG_DIR` plugin loading and its `tool.execute.before` hook (checked against opencode 1.18). A new opencode that changes either breaks the routing visibly (the plugin throws or does not load). |
| Load on users and agents | None for agents beyond what the sandbox already is. For operators and members: nothing to configure. |
| Release and compatibility | Additive. A core that advertises `sandboxExec` receives `execution` from the bridge for own drives in mode `all` and for borrowed drives per policy: members who set mode `all` and use an ACP agent that is not configured see a refusal where there was silent host execution; that is the point, and release notes say so. Mode `off` and `borrowed` for own drives are unchanged. |
| Observability | The core logs each sandboxed start (agent, harness, space; never the token) and each refusal with its reason; `bnbloc-acp check` reports per agent whether it can run sandboxed and why not; the agents status route carries the same. |
| AI capability | None added. |
| Component or platform | The core is a component in its own repository; the platform's sandbox contract is unchanged. A per-target `sandboxExec` flag (so the bridge could show the shield badge and the refusal per agent rather than per core) is a possible later contract change, noted in §5. |

**Pre-mortem.** It shipped and later failed. The most likely reasons:

1. *A sandboxed ACP turn ran a command on the host.* Mitigations: the confinement
   covers every process the agent spawns whatever its route; commands the
   plugin does not see still run confined (writes only the workspace); the core
   refuses any agent without a seam. The real-sandbox test starts the real
   confined agent and tries to write outside the workspace.
2. *The exec token expired mid-session and commands failed.* The core renews the
   token file at every input; a turn longer than one token lifetime relies on the
   bridge's renewal (`executionRefresh`, tracked in §5), which the sibling core
   handles with a file too.
3. *The routing plugin was not loaded after an opencode upgrade.* The warm-up is
   keyed by opencode's version, and an opt-in test runs the installed opencode
   and checks that a bash command is routed and leaves nothing on the host
   (`ACP_REAL_OPENCODE=1`, and `ACP_REAL_CLAUDE=1` for the Claude agent through npx). A runtime check, at a session's first turn, that its
   plugin took the call is **not built** (§5): until it is, a version that stops
   loading the plugin leaves bash confined but un-routed, and the test above is
   what catches it.
4. *A member set mode `all` and could no longer use an ACP agent.* The refusal
   names the setting (`sandbox`) and the agent; `bnbloc-acp check` says so before
   a turn is tried.
5. *The workdir workspace deleted a space.* Delete forgets the record and removes
   nothing in the directory; a test deletes a workdir workspace and finds the
   directory and its files intact.

## 4. Build order (slices of §2)

1. Config and capability (done): the routing recognised from the command, an optional `sandbox` override (validated against the routings
   the core implements), `sandboxExec` advertised when `vmexec` can confine,
   the per-agent refusal for create and input, `check` reporting.
2. Workdir workspaces (done): create at a directory, the record, delete that forgets only
   the record, the equality of workdir and execution, the refusal of a
   host session in a sandbox workspace.
3. The confined spawn (done): `confine` argv, the descriptor's environment, `PWD`, the
   token file renewed at every input, the descriptor pin and the restart on a
   change, terminals off. Tests with a fake `vmexec` that records argv and
   environment and runs the agent.
4. opencode routing (done): the plugin directory, the warm-up (polled, detached
   from the request), write-denies. Tests with the real `confine` (macOS) start a
   test agent and show a write outside the workspace, a read of the adapter's home
   and a change of the token file refused; an opt-in test runs the installed
   opencode (see Status). The runtime check of the first turn is open (§5).
5. Release: the ACP core's go.mod takes the bnbloc version with `denyRead`; the
   README and the design note's Sandbox section say what is now true.

## 5. Open questions

- **A runtime check that the routing took.** At a sandboxed session's first turn,
  run a harmless command through the agent's bash tool and refuse the session when
  it ran on the host. It needs an agent turn of the core's own (a model call per
  session) or a hook-level self-test; neither is built.
- **Commands that bypass the plugin.** The plugin hooks opencode's bash tool.
  Another route to a shell (a subagent's tool, an MCP server that runs commands,
  a plugin) is confined but runs on the host inside the confinement, not in the VM.
  This is the same limit the sibling core has for its opencode harness. A
  per-agent check that fails closed on an unrouted tool is not available from the
  hook's API today.
- **Long turns.** The bridge renews a running turn's token by `executionRefresh`
  for cores that advertise it; the ACP core would need to take the refreshed
  token into the file mid-turn. Until then a turn is bounded by one token
  lifetime (an hour).
- **A per-target flag.** `sandboxExec` is the core's. Let `core.Target` say per
  agent whether it can be sandboxed, so the bridge refuses (or shows the badge)
  per agent and a mixed config is described correctly.
- **Other CLIs through ACP** (claude's `CLAUDE_CODE_SHELL_PREFIX`, codex's exec
  server) are routings to add when an operator needs them; each is its own
  slice with its own verification.
