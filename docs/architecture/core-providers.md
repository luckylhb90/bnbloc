# Adding a local-agent core

This is the durable guide for adding a **core** — the local agent daemon bnbloc
drives over HTTP. A core joins either as manifest data (a JSON file
interpreted by `core/manifestcore`) or as a Core Protocol v1 adapter daemon;
any REST daemon can join one of these two ways. Read it before writing either
form.

## What a core provider IS

A core provider is the **outbound control seam** between bnbloc and one local
agent daemon. It is defined by `core.Provider` in
[`core/provider.go`](../../core/provider.go): neutral verbs that translate
onto some daemon's REST API.

```go
type Provider interface {
    Name() string
    CreateWorkspace(ctx context.Context, ep Endpoint) (workspaceID string, err error)
    CreateSession(ctx context.Context, ep Endpoint, req SessionRequest) (SessionResult, error)
    SendInput(ctx context.Context, ep Endpoint, workspaceID, sessionID, text string, opts SessionOptions) error
    ListTargets(ctx context.Context, ep Endpoint) ([]Target, error)
    Messages(ctx context.Context, ep Endpoint, workspaceID, sessionID string, after int64) ([]Message, int64, error)
    PollTurn(ctx context.Context, ep Endpoint, workspaceID, sessionID string) (TurnStatus, error)
}
```

Key properties, all enforced by the interface:

- **Outbound only.** The seam is bnbloc → core. Inbound activity (core →
  bnbloc) stays per-core glue that runs inside the core's own process,
  because it imports the core's packages and cannot be abstracted. See the
  [`core/provider.go`](../../core/provider.go) package doc.
- **Stateless.** Every method takes the member's `Endpoint` (`{BaseURL, Token}`)
  as an argument — the provider holds no connection or session state, because
  each member runs their own local daemon at their own address and token. One
  provider instance serves all members.
- **Identified by `Name()`.** The name is the registry key AND the target
  prefix. Target ids are **core-qualified**: `QualifyTargetID(core, rawID)`
  produces `"<core>/<rawid>"` (e.g. `acme/local:codex`,
  `nimbus/agent_abc123`). `SplitTargetID` splits on the FIRST slash only, so a
  core-local id may itself contain slashes. This is the single routing key that
  flows targets → chat recipient → bridge (see
  [`docs/prd/2026-09-11-multi-core-providers.md`](../prd/2026-09-11-multi-core-providers.md)).

### Optional capabilities

Beyond these verbs, a core may offer optional capabilities, each an
interface in `core/provider.go`: `TurnInterrupter` (`interrupt`),
`WorkspaceFileWriter` / `WorkspaceFileBytesWriter` (`writeFile`),
workspace deletion (`deleteWorkspace`), `SessionFiles` (`sessionFiles`: list
and read the agent session's workspace files, which the bridge publishes as
room artifacts — "deliverables"), `SessionWorkdirReporter` (`sessionWorkdir`:
the session's working directory, the bridge's default live-sync dir), plus
`Discoverer` (`discover`: the core locates its own daemon) and
`TargetResolver` (`resolveTarget`: target ids derive from a provider name),
both offered by a manifest that declares a `discover` clause / a
`targetIdPrefix`. `sessionFiles` (+ `sessionFileBytes`) is offered by a
manifest that declares the session-file ops and by a Core Protocol daemon that
advertises it (see "Core Protocol adapter daemon" below). `sessionWorkdir` has
no Core Protocol v1 endpoint, so only a manifest offers it. A core without a
capability gets no deliverable publishing / default sync dir (debug-logged)
instead of a guessed REST shape. Callers never type-assert a provider directly — they ask
`core.As[T](ctx, p, ep, capability)`, which returns the interface only when the
provider implements it AND, for a data-described provider (a manifest's
declared ops, a Core Protocol daemon's discovery document, both
`CapabilityReporter`s), reports that capability supported for that endpoint.
A provider therefore never appears to offer what its manifest or daemon does
not; there are no per-capability wrapper types.

`sandboxExec` is the one capability that changes what a request CARRIES
rather than which method is called, so it has no interface: callers ask
`core.Supports(ctx, p, ep, core.CapabilitySandboxExec)`. A core advertising it
receives an `execution` descriptor (`core/protocol` `Execution`, in process
`core.SessionOptions.Execution`) on session create AND every follow-up input
of a sandboxed session:

```json
{"mode": "sandbox", "sandbox": {
  "url": "http://127.0.0.1:7311", "execToken": "…", "expiresAtMs": 0,
  "machine": "bnbloc", "space": "…", "vmexec": "/abs/bnbloc-vmexec",
  "workdir": "/Users/m/bnbloc-workspaces/<space>",
  "mounts": [{"host": "/Users/m/bnbloc-workspaces/<space>", "guest": "/Users/m/bnbloc-workspaces/<space>"}],
  "passthrough": ["gh"],
  "hostConfine": {"workspace": "/Users/m/bnbloc-workspaces/<space>", "allowRead": [], "allowWrite": [],
                  "denyWrite": ["/Users/m/bnbloc-workspaces/<space>/.routing-config"]}}}
```

The core keeps the agent CLI on the host, runs it in `workdir` confined with
`bnbloc-vmexec confine` (Seatbelt on macOS, bubblewrap on Linux; a
`denyWrite` path stays read-only even before it exists, and an entry ending
in `/` names a directory), and routes
the CLI's shell commands into the space through `bnbloc-vmexec` (`run`,
`claude-prefix` for `CLAUDE_CODE_SHELL_PREFIX`, `hook <harness>` for
command-rewriting hooks); file tools, MCP servers and hooks stay on the host.
`protocol.VMExecEnv` turns the descriptor into the helper's environment
(`BNBLOC_VMEXEC_*`, `BNBLOC_CONFINE_*`). `execToken` is a short-lived
space-scoped Sandbox Protocol exec token (it can only run commands in that
space) — never log or persist it; a fresh one arrives every turn. A core must
answer `bad_request` for an execution it cannot honor (never silently run on
the host); absent `execution` = run on the host as before. bnbloc never sends
one to a core that does not advertise `sandboxExec`: manifest cores and the
protocol client refuse it with `core.CapabilityMissingError`. The workspace of a
sandboxed session is created IN `workdir` (`CreateWorkspaceRequest.workdir`,
`core.WorkdirWorkspaceCreator`, offered with `sandboxExec`) so the `.room/`
context and attachments the bridge writes before the first turn land where
the confined CLI and the guest see them; `execution.sandbox.workdir` must equal
it. Design:
[`docs/prd/2026-10-09-agent-vm-sandbox.md`](../prd/2026-10-09-agent-vm-sandbox.md).

`agentProfile` is the second request-carrying capability (also asked through
`core.Supports`): a core advertising it receives a `profile` on session
create (`core/protocol` `AgentProfile`, in process `core.SessionRequest.Profile`),
the role the session wears (an expert, the skill packs in effect, the MCP
tool components they require, and MCP servers contributed per session such
as room tools):

```json
{"profile": {
  "instructions": "You are the reviewer. …",
  "skills": [{"name": "review-checklist", "files": [
    {"path": "SKILL.md", "content": "---\nname: review-checklist\ndescription: …\n---\n…"},
    {"path": "scripts/lint.sh", "content": "…"},
    {"path": "assets/logo.png", "contentBase64": "…"}]}],
  "mcpServers": [
    {"type": "http", "name": "room", "url": "http://127.0.0.1:41234/mcp",
     "headers": [{"name": "Authorization", "value": "Bearer …"}]},
    {"name": "fs", "command": "mcp-fs", "args": ["--root", "."], "env": []}],
  "env": {"TOOL_TOKEN": "…"}}}
```

The core maps it natively and keeps the member's own agent configuration
loading alongside: the instructions as a system-prompt append (ACP
`_meta.systemPrompt.append`, a CLI flag) or an instructions file in the
workspace (`AGENTS.md`, `CLAUDE.md`); each skill as
`<skills dir>/<name>/<path>` (`.claude/skills/`, `.agents/skills/`,
`.opencode/skills/`) so the agent loads it from `SKILL.md`'s description; the
MCP servers in the ACP `McpServer` shape (stdio has no `type`); `env` as
extra agent environment. Env values and MCP header/env values are secrets: a
core never logs or persists them. A core that cannot honor a profile answers
`bad_request`; manifest cores and the protocol client refuse to send one to a
core that does not offer the capability (`core.CapabilityMissingError`, the
same error as any other absent capability). A profile is per session: the bridge starts a new
session when the content (instructions + skill files) or the set of MCP tool
components (name, transport, binary or URL, the names of the env and headers
they get, never their values) changes. A stdio MCP tool's command is a binary
the member's desktop materialized and verified
([`content-components.md`](content-components.md#mcp-tool-components)): a
profile never carries a command that content inlined. A core without
`agentProfile` gets the bridge's fallback: the instructions (and a summary of
the skills) prefixed to the first prompt of the session.

Each target reports the MCP transports its agent accepts in a profile:
`core.Target.MCPTransports` (wire `mcpTransports`: `"stdio"`, `"http"`,
`"sse"`; empty = stdio only, which every ACP agent takes). It is per target
because an adapter fronting several agents learns each agent's support
separately (ACP `initialize` → `agentCapabilities.mcpCapabilities`). The
bridge hands it to session contributors (`SessionInfo.MCPTransports`), so a
contributor attaches a server the agent can reach instead of the core
dropping it. A manifest declares it as `listTargets.targets.mcpTransports`
(a list element's own `mcpTransports` wins).

**Workspace path policy** (`core/workspace_paths.go`). The `writeFile` path
contract is `core.BnblocOwnedPaths`: `.room/` plus the agent config a
session wears (`CLAUDE.md`, `AGENTS.md`, `.claude/skills/**`,
`.agents/skills/**`, `.opencode/**`, `.mcp.json`); `core.WorkspaceFileAllowed`
is the rule a core enforces on every write, and both clients refuse any other
path before the wire (`core.CheckWorkspaceFile`). Beside it,
`core.HostExecutableProjectConfigPaths` names the project config a harness
executes on the host (`.claude/**`, `CLAUDE.local.md`, `.codex/**`,
`opencode.json(c)`). Neither kind is the room's project
(`core.ProtectedWorkspacePath`): the bridge never publishes, site-snapshots,
code-syncs (either direction) or packs them into a component, and both are
write-denied in sandboxed runs (`hostConfine.denyWrite`) and cleared from a
sandbox workdir before a fresh session.

Vocabulary note: the codebase overloads "provider". **Core** = the daemon
backend (an installed manifest or adapter core such as `acme`), carried on the wire as `core`. **Harness** = the
agent kind inside a core (`codex`, `claude`), carried in `core.Target.Provider`
and on the wire as `provider`.

## Two ways to add a core (no core is compiled in)

No core ships inside the gateway/bridge binaries — neither manifest data nor
an adapter daemon. Pick the form the wire contract fits:

| Way | Recompile? | Use when |
| --- | --- | --- |
| 1. Manifest | No | The daemon's control path is request/response REST templating — including listener-file discovery and per-target catalog enrichment (the common case). |
| 2. Core Protocol adapter daemon | No (own binary) | The lifecycle is procedural — multi-step handshakes, wait loops, or state no template can host. |

An agent that speaks the Agent Client Protocol needs neither: it is a
configuration entry of the generic ACP core (see "ACP agents" below).

### 1. Manifest core (no recompile)

A manifest core is distributed as a **signed package**: wrap the manifest JSON
in a `bnbloc.core.package/v1` envelope (runtime `manifest`) and install it as
a component — Platform console → Components, `POST /v1/platform/components`,
or the CLI (`bnbloc cores install --manifest <file> --name <name>`). The
gateway validates, verifies the signature against a trusted publisher key,
persists the record to platform settings, rehydrates it at every boot, and
`core.Reregister`s it — usable immediately, no restart.
`DELETE /v1/platform/components/{name}` uninstalls it. The CLI's `--manifest` path
signs for you with `--seckey`, or uses `--unsigned` against a dev gateway
(`BNBLOC_EXT_ALLOW_UNSIGNED`).

A manifest can express listener-descriptor discovery with a docker
host-rewrite, a target-id prefix (e.g. `local:`), per-target catalog
enrichment, a settled-turn idle predicate, the `deleteWorkspace` op, and the
session-file / session-workdir ops, all as data. The schema below and the
worked example further down are the reference.

#### Manifest schema

Types are in [`core/manifestcore/manifestcore.go`](../../core/manifestcore/manifestcore.go)
(`Manifest`, `Op`, `TargetsSpec`, `IdlePredicate`, `DiscoverOp`). The six
verbs `createWorkspace`, `createSession`, `sendInput`, `listTargets`,
`messages` and `pollTurn` are required, each with a `method` and `path`, and
`messages` needs `items.list` + `items.fields.text`, or the manifest fails
validation at load (not at first agent call): a core whose replies cannot be
read would time every turn out silently.

| Field | Applies to | Meaning |
| --- | --- | --- |
| `name` | manifest | Registry key and target prefix. Required, non-empty, must not collide with a built-in. |
| `discover` | manifest | Optional. Either a fixed-URL liveness probe `{probeUrl, probePath}` (HTTP 200 at `probeUrl+probePath` ⇒ found) or a listener descriptor `{listener: {defaultPath, pathEnv, addrField, tokenField}, hostRewrite: {env, default}}` (see Discovery below). Omit for config-only cores. |
| `method`, `path` | every op | HTTP method and path template. Both required. |
| `body` | request ops | Optional JSON request-body template (see template vars). |
| `extract` | ops with a result | Map of result key → dotted JSON path in the response. Keys read: `workspaceId` (createWorkspace); `sessionId`, `turnId`, `phase` (createSession). |
| `targets` | list ops | `{list, id, provider}`: `list` is the dotted path to the array; `id`/`provider` are field names within each element. |
| `idleWhen` | pollTurn | `{field, notIn, unlessSet}`: reads `field` and reports idle when its value is NOT in `notIn` (or when `unlessSet` names a present field: a settled turn). Absent ⇒ always idle. |
| `items` | messages, listSessionFiles | `{list, fields, completedWhen}`: `list` is the dotted path to the array; `fields` maps each record field to a dotted path within one element. |

Response mapping per verb:

- `createWorkspace` → `extract.workspaceId`.
- `createSession` → `extract.sessionId` (required; empty ⇒ error), plus optional
  `turnId`, `phase`.
- `sendInput` → no result (any 2xx is success).
- `listTargets` → `targets` shapes the array into `{id, provider}` targets;
  elements with an empty id are dropped.
- `messages` → `items` with `fields` `version`, `role`, `text`, `userVisible`,
  `messageId`, `seq`, `createdAtMs`, `thinking`, `metrics` and
  `completedWhen`; `extract.latestVersion` is the cursor.
- `pollTurn` → `idleWhen` predicate over the response; a settled turn whose
  `extract.failed` reads `failed` reports `extract.failMessage` (else
  `extract.failCode`).

Optional ops (absent ⇒ capability not offered; see Optional capabilities):

| Op | Capability | Vars | Response mapping |
| --- | --- | --- | --- |
| `interrupt` | `interrupt` | `{workspaceId}`, `{sessionId}` | none (2xx = success) |
| `writeWorkspaceFile` | `writeFile` | `{workspaceId}`, `{relPath}`, `{content}`, `{contentBase64}` | none |
| `deleteWorkspace` | `deleteWorkspace` | `{workspaceId}` | none |
| `listSessionFiles` | `sessionFiles` | `{workspaceId}`, `{sessionId}` | `items`: `list` + `fields.path` (required), `fields.bytes`, `fields.mtimeMs`; elements with an empty path are dropped |
| `readSessionFile` | `sessionFiles` (+ `sessionFileBytes` with `contentBase64`) | `{workspaceId}`, `{sessionId}`, `{relPath}` | `extract.content`: the file's text, or `extract.contentBase64`: the file's exact bytes, base64 (one is required). Only `contentBase64` is binary-safe: the bridge publishes binary deliverables (images, PDFs, archives) only from a core that declares it; a text-only core publishes known text files only |
| `sessionWorkdir` | `sessionWorkdir` | `{workspaceId}`, `{sessionId}` | `extract.workdir` (required): the session's working directory on the core host |

`listSessionFiles` and `readSessionFile` must be declared together, and each
of the three needs its required mapping, or the manifest fails validation at
load/install. Example:

```json
"listSessionFiles": { "method": "GET",
  "path": "/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}/files",
  "items": { "list": "files", "fields": { "path": "path", "bytes": "bytes", "mtimeMs": "mtimeMs" } } },
"readSessionFile": { "method": "GET",
  "path": "/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}/file?path={relPath}",
  "extract": { "content": "content" } },
"sessionWorkdir": { "method": "GET",
  "path": "/v1/workspaces/{workspaceId}/agent-sessions/{sessionId}",
  "extract": { "workdir": "session.cwd" } }
```

#### Template vars

`{var}` placeholders substitute in the `path` string and as whole string values
inside the `body` JSON (see [`core/manifestcore/template.go`](../../core/manifestcore/template.go)).
An unknown placeholder renders empty. In a `path` (and its `pathWhen`
fragments) each substituted value is escaped for where it lands: before the
template's first literal `?` it is path-escaped (an id cannot split a
segment), after it query-escaped (a file name with `&`, `#`, `+` or a space
reads the right file). Template literals are never escaped; body values are
substituted verbatim. Available vars, per verb:

| Verb | Vars available |
| --- | --- |
| `createWorkspace` | `{shortid}` |
| `createSession` | `{workspaceId}`, `{targetId}`, `{prompt}`, `{shortid}`, `{model}`, `{reasoningEffort}`, `{instructions}`, and the raw-JSON forms `"{agentProfile\|json}"`, `"{skills\|json}"`, `"{mcpServers\|json}"`, `"{env\|json}"` |
| `sendInput` | `{workspaceId}`, `{sessionId}`, `{text}` |
| `messages` | `{workspaceId}`, `{sessionId}`, `{after}` |
| `pollTurn` | `{workspaceId}`, `{sessionId}` |

The `{targetId|firstTarget}` fallback form: when a `createSession` body
references it and the request supplies no target, the provider calls
`listTargets` first and binds the first listed target's id. Use it for daemons
that require a pre-registered agent id.

The exact string `"{var|json}"` inserts the var's JSON value (an object or
array in the Core Protocol wire shape, `null` when empty) instead of a string;
`"{var|omit}"` drops the key when the var is empty. A manifest whose
`createSession` body references any profile var (`{instructions}`,
`agentProfile`, `skills`, `mcpServers`, `env`) advertises `agentProfile`.

#### JSON-path extraction

Paths are dotted (`a.b.c`) against the decoded response. Leaves are coerced to
strings, so numbers/bools extract forgivingly. For `targets`, `list` is the
dotted path to the array; `id`/`provider` are keys read from each element
(see `jsonPath`/`jsonPathString` in
[`core/manifestcore/template.go`](../../core/manifestcore/template.go)).

#### Discovery, idle, auth

- **Discovery** (`discover`) takes one of two forms.
  - A **probe**: a GET liveness probe; on 200 the core's endpoint becomes
    `{BaseURL: probeUrl, Token: probeToken}`. The token is deployment config,
    injected by the composition root from `BNBLOC_CORE_<NAME>_TOKEN`.
  - A **listener descriptor**: for a daemon that binds a random port and
    rewrites a JSON file with its address and token on every start.
    `listener.defaultPath` locates the file (a `~/` prefix expands to the
    user's home directory), so a host-side bridge such as the desktop sidecar
    finds the daemon from the manifest alone. `listener.pathEnv` optionally
    names a variable that overrides the path when non-empty; `addrField` and
    `tokenField` are dotted paths into the file. `hostRewrite` replaces the
    loopback host of the discovered address (e.g. with
    `host.docker.internal` for a containerized caller): `default` applies
    when the variable named by `hostRewrite.env` is unset, and setting that
    variable to empty disables the rewrite. These variable names belong to
    the manifest, not to bnbloc.
- **Idle predicate** (`idleWhen`) turns a status field into the busy/idle
  boolean.
- **Auth** is bearer-only: when the endpoint carries a token, requests send
  `Authorization: Bearer <token>`.

#### Worked example (adapter-equivalent manifest)

This manifest reproduces a protocol adapter's required verbs. Wrap it
in a core package and install it
(e.g. `bnbloc cores install --manifest myrest.json --name myrest`, then
publish with rollout or add `myrest` to `BNBLOC_CORES` if you drive it by
explicit URL):

```json
{
  "name": "myrest",
  "discover": {
    "probeUrl": "http://host.docker.internal:6767",
    "probePath": "/v1/info"
  },
  "createWorkspace": {
    "method": "POST",
    "path": "/v1/projects",
    "body": { "name": "bnbloc-{shortid}" },
    "extract": { "workspaceId": "id" }
  },
  "createSession": {
    "method": "POST",
    "path": "/v1/sessions",
    "body": {
      "agent_id": "{targetId|firstTarget}",
      "project_id": "{workspaceId}",
      "initial_items": [
        {
          "type": "message",
          "data": {
            "role": "user",
            "content": [{ "type": "input_text", "text": "{prompt}" }]
          }
        }
      ]
    },
    "extract": {
      "sessionId": "id",
      "turnId": "active_response_id",
      "phase": "status"
    }
  },
  "sendInput": {
    "method": "POST",
    "path": "/v1/sessions/{sessionId}/events",
    "body": {
      "type": "message",
      "data": {
        "role": "user",
        "content": [{ "type": "input_text", "text": "{text}" }]
      }
    }
  },
  "listTargets": {
    "method": "GET",
    "path": "/v1/agents",
    "targets": { "list": "data", "id": "id", "provider": "harness" }
  },
  "messages": {
    "method": "GET",
    "path": "/v1/sessions/{sessionId}/messages?after={after}",
    "items": {
      "list": "data",
      "fields": { "version": "version", "role": "role", "text": "text" },
      "completedWhen": { "field": "status", "equalsAnyOf": ["completed"] }
    },
    "extract": { "latestVersion": "latest_version" }
  },
  "pollTurn": {
    "method": "GET",
    "path": "/v1/sessions/{sessionId}",
    "idleWhen": { "field": "status", "notIn": ["running"] }
  }
}
```

#### Manifest limits

A manifest expresses only what data can express:

- No multi-step handshakes (each verb is one HTTP call).
- No streaming control.
- No exotic auth (bearer token only).
- Cannot take over a name another registered core already owns.

When a core needs control flow — for example, picking an online host, then
waiting for a runner to report online, then delivering the prompt — that core
takes the adapter form below, not a more complex manifest.

### 2. Core Protocol adapter daemon

For a core the manifest cannot host, ship an **out-of-process adapter** that
speaks Core Protocol v1 (bnbloc's own wire contract:
[`core/protocol`](../../core/protocol)) and drives the real daemon underneath.
The bridge/gateway drive it through the protocol client with
`BNBLOC_CORE_<NAME>_URL` — no compile-time coupling, no recompile of bnbloc.

1. Implement the protocol routes (`/core/v1/discover`, `targets`, `workspaces`,
   `sessions`, `input`, `messages`, `turn`) with the exact
   wire shapes and error envelope from `core/protocol/types.go`; import the
   package rather than redeclaring them. The wire format is the types' explicit
   camelCase JSON tags (`workspaceId`, `sessionId`, …) — that file IS the
   contract, so every wire field carries a tag and an untagged field is a bug.
   The envelope's `retryable` is optional: state it only when you know;
   absent, bnbloc retries a 429/503 that started no work (`core.Retryable`).
   Advertise only the optional
   capabilities you actually serve (`interrupt`, `writeFile`,
   `deleteWorkspace`, `sandboxExec`, `agentProfile`, `sessionFiles` + `sessionFileBytes`) —
   clients degrade gracefully when a capability is absent.
   **Deliverables** (`sessionFiles` + `sessionFileBytes`, advertised
   together — the wire has only the exact-bytes read):
   `GET /core/v1/workspaces/{ws}/sessions/{sid}/files` →
   `{"files":[{"path","sizeBytes","modTimeMs"}],"truncated"}` (regular files
   only, workspace-relative slash paths, no symlinks; the core applies its
   own ignore rules — `.git`, hidden dirs incl. the bridge's `.room/`,
   `node_modules`, `.venv`/`venv`, `target` — and caps at
   `protocol.MaxSessionFiles` (2000) with `truncated: true`; web build output
   — `dist`, `build`, `out` — must stay listed: the bridge publishes a built
   static web app there as one `site` snapshot and keeps those files out of
   the individual deliverables itself);
   `GET /core/v1/workspaces/{ws}/files/{path}` (the same route `writeFile`
   PUTs; the path arrives as one escaped segment) → the raw bytes with a
   sniffed `Content-Type` and `Content-Length`; a path that is absolute or
   contains `..` is `400 bad_request`, a missing file / directory / anything
   resolving outside the workspace (symlinks included) is
   `404 file_not_found`, a file over the core's read cap is
   `413 file_too_large`. With both advertised the bridge publishes the
   core's deliverables, binaries included. Core Protocol v1 has no
   session-workdir endpoint, so a protocol core supplies no default sync dir.
2. Drive the real daemon inside. A stateless translator can map protocol
   workspace ids and session ids directly to upstream ids, forward caller bearer
   tokens unchanged, and translate upstream status into protocol error codes. A
   stateful adapter can additionally supervise local processes and own model
   discovery.
3. Prove it against the contract: run the real `core/protocol` client and the
   `core/conformance` suite against your adapter over a mock upstream.
4. Distribute: a server deployment may run it as its own service and point
   `BNBLOC_CORE_<NAME>_URL` at it (the generic composes ship no adapter); the
   desktop runs it as a published sidecar core package
   (`bnbloc cores publish-sidecar`), with the gateway managing listing +
   artifact distribution.

There are no native Go providers: the registry has no compiled-in (built-in)
source, and `core.Register` is gone (2026-10-12). A compiled-in core would
recompile and re-ship every binary, which is exactly what the adapter forms
remove. Go's `plugin` package
is **permanently out** — Windows-unsupported and toolchain-locked, and Windows
is a repository compatibility contract.

### ACP agents (the ACP core)

Agents that speak the [Agent Client Protocol](https://agentclientprotocol.com)
(Claude Code, Codex, Gemini CLI, Qwen Code, OpenCode, Kimi, …) join through
[**bnbloc-acp**](https://github.com/luckylhb90/bnbloc-acp), an open-source
Core Protocol v1 adapter daemon in its own repository. It ships as a desktop
sidecar component; each member lists the ACP agents installed on their
machine in their own `agents.json`, so adding an agent is a config entry, not
code. Configuration, the permission policy, packaging and its environment
variables are documented there. Design record:
[`docs/prd/2026-10-11-acp-core.md`](../prd/2026-10-11-acp-core.md).

## Configuration and discovery

Both the gateway and bridge build their core list from the same code,
[`coreset.FromEnv`](../../internal/coreset/coreset.go), so they agree byte-for-
byte on selection and addressing.

| Variable | Meaning |
| --- | --- |
| `BNBLOC_CORES` | Comma-separated core names to run, in order (first = default core). Installs need no entry; this is the operator escape hatch for explicitly-configured cores. |
| `BNBLOC_CORE_<NAME>_URL` | Explicit endpoint override for core `<NAME>` (uppercased). Wins over discovery. |
| `BNBLOC_CORE_<NAME>_TOKEN` | Explicit token for core `<NAME>`; also used as the discovery probe token. |
| `BNBLOC_CORE_MANIFESTS_DIR` | Transient manifest directory, written only by the internal desktop's sidecar manager (gateway-published manifests for its bridge); the DB is the source of truth. Not an install path, and nothing in the composes mounts one. |

Endpoint resolution per core, in order:

1. An explicit `BNBLOC_CORE_<NAME>_URL` (with optional `_TOKEN`) wins.
2. Otherwise, if the provider offers `discover`, discovery fills the endpoint
   best-effort through `coreset.Discover` (bounded by `DiscoverTimeout`, 3s, so
   a slow/absent daemon cannot stall startup; the same bound applies to every
   rediscovery and to the gateway's dev discover route).
3. Otherwise the endpoint is zero — the core is still listed; its targets simply
   come back empty until the daemon is up.

**Unknown core names are skipped with a loud warning, never fatal.** A typo or a
not-yet-loaded manifest/extension name in user-editable settings must not
crash-loop the bridge or gateway: `FromEnv` logs `skipping unknown core` and
continues with the cores that ARE registered.

## How a core surfaces

- **`coreset.FromEnv`** builds the ordered `[]Entry{Name, Provider, Endpoint}`
  list consumed by both the gateway and bridge composition roots.
- **Gateway** aggregates `GET /v1/local-agent/targets` across all cores, prefixes
  each returned target id with `<core>/`, and labels each target JSON with its
  `core` (shape `{id, provider, core}`). The web groups targets by core.
- **Bridge** routes each chat by `recipient.core` (falling back to the default
  core when absent) to that core's provider + endpoint, and keys sessions by
  `room\x00core\x00harness` so two cores exposing `codex` never collide.
- **`GET /v1/cores`** (served by the gateway itself, no room scope) lists every
  registered core name from `core.Names()` with `available` (the gateway has a
  resolved endpoint for it) and `builtin` (always false today — nothing is
  compiled in; installed manifest/endpoint/sidecar cores are not builtin). The
  desktop BridgePanel uses it as a validated datalist/suggestion for the
  core-name field while still allowing free text (so a not-yet-loaded name
  can be pre-entered). Installed components additionally carry their rollout
  state and are filtered by the member gate (see below).

## Verification

Follow the existing patterns rather than inventing new harnesses:

- **Manifest cores** —
  [`core/manifestcore/manifestcore_test.go`](../../core/manifestcore/manifestcore_test.go):
  `TestManifestReproducesRESTDaemon` drives a manifest against an `httptest` fake
  daemon and asserts the emitted method/path/body plus response extraction
  for the required verbs; `TestManifestIdlePredicate` covers the idle predicate;
  `TestLoadManifestsRegisters` / `TestLoadManifestsMalformedAndReregister`
  cover manifest loading, and `TestCapabilitiesFollowDeclaredOps` pins that capabilities follow the
  declared ops. `TestManifestSessionFileOps` / `TestManifestSessionFileOpsValidation`
  cover the session-file and workdir ops (rendering, escaping, mapping,
  load-time validation); `TestDiscoverListener` covers listener-descriptor
  discovery and the host rewrite.
- **Core Protocol adapters** — the `core/conformance` suite plus the real
  `core/protocol` client against your adapter over a mock upstream. The ACP
  core's tests ([bnbloc-acp](https://github.com/luckylhb90/bnbloc-acp)) run the test binary
  itself as a fake ACP agent (`fakeagent_test.go`); `TestConformance` runs
  the live suite over it.
- **Config + discovery** —
  [`internal/coreset/coreset_test.go`](../../internal/coreset/coreset_test.go):
  `TestFromEnvExplicitCoreListed`, `TestFromEnvMultiCoreOrderAndOverride`,
  `TestFromEnvRegistersAdapterDescriptorForURLDeclaredCore`, and the
  unknown-core cases (`TestFromEnvUnknownCore`, `TestFromEnvAllUnknownCores`)
  prove selection, override precedence, URL-declared listing, and that
  unknown names are skipped rather than fatal.

## Windows impact

None. The manifest interpreter is pure Go templating over `net/http`; manifest
paths use `filepath`; there is no `plugin`, no shell. OS specifics (listener
paths, host rewrites) are manifest data or composition-root config, never in
`core/`.

## Managed sidecar extras (desktop)

- **Optional sign-in status.** A sidecar that needs its own user sign-in may expose
  `GET {localUrl}/v1/auth/status` → `{"authenticated": bool, "user": string,
  "checkedAtMs": number}` (and `POST {localUrl}/v1/auth/login` to start sign-in). The
  desktop shows/starts sign-in only when `authenticated` is false and
  `checkedAtMs > 0`; `checkedAtMs: 0` means the first check is pending; 404 means the
  sidecar has no sign-in concept.
- **Target listings must be cheap.** The bridge and desktop poll targets; serve them
  from a cached snapshot rather than discovering per request.
- **Orphan replacement.** The desktop records each spawned sidecar (pid, artifact
  sha256) and replaces a listener on the fixed port that it spawned in an earlier
  session; it never signals a process it did not spawn (those are reused only when
  discover matches name and protocol, otherwise the sidecar waits).
