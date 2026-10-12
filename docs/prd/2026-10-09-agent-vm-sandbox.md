# Agent VM sandbox (2026-10-09)

Status: phase 1 part A landed (Sandbox Protocol v1, sandbox component kind,
conformance suite, local libkrun sidecar) — see "Phase 1A status" below.

Decisions (2026-10-09, product owner): local VMs first; a high-performance
design; the VM runtime is installed as a Component; the design must extend to
remote VMs.

## Why

Agents run on members' machines with the member's full user rights. Borrowing
makes this a cross-user risk: someone other than the owner sends prompts that
execute on the owner's machine (prompt injection in shared room chat becomes a
remote-execution path). A real isolation boundary is required at least for
borrowed drives, and is valuable for every unattended agent turn.

## What we learned

**A local agent runtime we studied** has no VM runtime in its open-source
repo: its VM is a closed-source desktop product (a desktop daemon plus a guest
agent). Its public contracts still show the shape worth copying:
- one local Linux VM per member (macOS/Windows), cloud keeps only room state;
- host↔guest over a guest-agent on vsock; room workspaces mounted in the guest,
  one Linux user per room;
- the VM image is a separately downloaded, sha-verified runtime artifact with
  channels (base/stable/active), prewarm, reset, kernel-panic detection;
- credentials never enter the VM: account sessions stay on the host behind a
  typed relay ("relay failure must not fall back to the VM holding a session");
- the same runtime-preparation code runs in the guest; guest service ports
  become room previews.

**Industry (2026)** converges on: a VM per session/task in the cloud
(Firecracker/Kata/gVisor), OS sandboxes (Seatbelt, bubblewrap+Landlock) or
microVMs locally (Docker Sandboxes, microsandbox/libkrun, Apple
Containerization), and a default-deny egress proxy **outside** the guest that
substitutes credentials so keys never enter it. Fast start comes from
snapshots + warm pools. Kubernetes has a native primitive:
kubernetes-sigs/agent-sandbox (Template / WarmPool / Claim) on Kata or gVisor.

## Proposed design

### Seam: Sandbox Protocol v1 + a sandbox component kind
One HTTP contract (like Core Protocol v1) — create / start / stop / reset /
exec (streamed) / mounts / port forwards / prewarm / snapshot / egress policy,
with optional capabilities advertised by a discover document — implemented by:
- **local**: a VM-runtime sidecar package (`runtime: "sidecar"`), installed
  through Components, supervised by the desktop sidecar manager (artifact
  sha256 pinning, rollout/optin, tenant scope as for every component);
- **remote** (later): an endpoint package (`runtime: "endpoint"`) for a
  server-side implementation (own Kubernetes or a hosted provider adapter).
The desktop and bridge only speak the protocol, never a specific VMM.
(Superseded by the hybrid revision below: the core stays on the host and only
its shell commands run in the guest.) Space key = (room, core, provider) —
the bridge session key.

### Local backends (best available first)
| Host | Backend | Notes |
|---|---|---|
| macOS Apple silicon | libkrun (via microsandbox) | embeddable library, OCI images, virtio-fs, <1 s boot |
| Linux | libkrun on KVM | same code path |
| Windows | libkrun/microsandbox on WHP | never WSL2 (not a security boundary) |
| macOS Intel | Lima `vz` VM | heavier, but runs on Intel macOS 13.5+ |
| any (fallback) | Seatbelt / bubblewrap profile | labelled "weak"; refused for borrowed drives unless the owner opts in |

### Guest image
An OCI image per core family (base Linux + toolchains + the core binary),
published as a Components artifact (sha256-pinned, rollout/optin like other
components), cached on the machine, prewarmed at app start.

### Workspace
Clone mode: the agent works in a private copy inside the guest; results come
back as files/artifacts and a git branch/patch. A borrowed drive gets a fresh
fork per drive so a borrower cannot persist state in the owner's workspace.

### Egress and credentials
Host-side egress proxy, default deny; allowlist from platform/admin config
(never hardcoded). Model API keys and git tokens are substituted on the host
(the guest only sees placeholders). Subscription OAuth logins (Claude.ai /
ChatGPT CLIs refresh tokens themselves) need a spike: they may require the
login to live in the guest with a scoped, revocable token.

### Policy (revised 2026-10-09)
Running an agent in a VM means a Linux build of its CLI in the guest image and
none of the member's local setup (skills, plugins, MCP servers, settings,
macOS-only tools) unless it is projected in. So:
- **Own agent (default): runs natively on the host as today** — full local
  skills/plugins/MCP/login, no change.
- **Borrowed drives: VM required** (fail closed when no VM backend is
  available). The guest gets:
  - the CLI's Linux build baked into the image, version-matched to the host;
  - the owner's file-based setup projected read-only at the same paths
    (skills, prompts/commands, CLAUDE.md/AGENTS.md, non-secret settings);
  - credentials only as placeholders (host-brokered substitution on egress);
  - MCP servers and credential-bearing plugins **off by default** for
    borrowers; the owner opts in per item, and opted-in servers are relayed
    from the host (their credentials stay on the host).
- **Own agent in a VM: optional** member setting (same projection rules).
- Admin policy can require VMs for the tenant.

Why not "agent on host, commands in the VM": agent file-edit tools write the
host filesystem directly, so only shell commands would be isolated — not a
boundary for borrowed prompts.

### Server-side sandboxes (later phase)
For members without a capable machine: same seam, a server backend.
Phase A: hosted provider (E2B / Cloudflare) behind the seam.
Phase B: own Kubernetes — agent-sandbox CRDs + Kata/Cloud Hypervisor on a
KVM node pool, gVisor fallback, per-sandbox egress proxy.

## Phases
1. Seam + macOS Apple-silicon libkrun backend + guest image for the default
   sidecar core + clone-mode workspace + host egress proxy + borrowed-drive
   policy.
2. Linux + Windows backends, Intel-Mac Lima backend, weak fallback tier.
3. Snapshots / prewarm pool, per-drive forks, preview port forwarding.
4. Server-side sandboxes.

## Revision: hybrid execution (2026-10-09, chosen)

Product owner rejected reinstalling agents inside the VM. Chosen design
("option A"): **the agent CLI stays on the host** (its own install, skills,
plugins, MCP servers, login) confined by an OS sandbox (Seatbelt on macOS) to
the workspace; **the shell commands it runs execute inside one shared Linux
VM** (per-room Linux user + directories + egress rules). Credentials never
enter the VM.

Spike evidence (M4 Pro, real turns, scratch/vm-spike):

| CLI | Command routing | Verified |
|---|---|---|
| Claude Code | `CLAUDE_CODE_SHELL_PREFIX` router: Bash-tool commands → VM; hooks, stdio MCP, status line stay on host | yes |
| Codex | native `codex exec-server` in the VM + `CODEX_EXEC_SERVER_URL` on the host | yes |
| hook-capable sidecar CLI | PreToolUse hook rewriting the command | yes |
| opencode | plugin hook `tool.execute.before` | yes |

- Per-command overhead ~20–24 ms when spawning the runtime CLI per command
  (`msb exec`, p95 ~120 ms); the phase-1A sidecar keeps a persistent agent
  channel instead: p50 0.9 ms, p95 1.2 ms (see below). stdio/exit code
  faithful.
- Workspace: host source tree shared into the guest at the identical path
  (virtio-fs); heavy generated dirs (node_modules, .venv, target, caches) on
  the guest disk via bind mounts. go build 5.5 s vs 5.1 s, npm install 1.7 s vs
  1.0 s, pytest 0.84 s vs 0.31 s (shared vs guest disk); bulk small-file
  extraction is the slow case.
- Seatbelt profile for the host CLI works for a real turn (workspace RW;
  ~/.ssh, other projects, keychain denied; deny `file-read-data`, not
  `file-read*`). Allow installed plugin/skill source dirs automatically.
- Commands that call host-only tools (e.g. skills using host CLIs) need a
  host-passthrough allowlist: on for the owner's own runs, off for borrowed
  drives unless the owner allows.
- File tools, grep/glob, web fetch, MCP and language servers stay on the host.

Phase 1 scope: VM runtime component (the libkrun sidecar, Components-installed,
prewarmed), per-CLI command routing in the cores, host Seatbelt profile with
plugin-dir allowance and passthrough allowlist, desktop switch/status;
default on for borrowed drives, optional for own agents.

## Phase 1A status (2026-10-09)

Landed in this repo (public seam, no desktop/bridge integration yet):

- **Sandbox Protocol v1** — `sandbox/protocol` (wire types + Go client) and
  [`docs/architecture/sandbox-protocol.md`](../architecture/sandbox-protocol.md).
  Modelled on the hybrid design: one long-lived **machine** per member
  computer; rooms are **spaces** inside it (per-room guest user, workspace
  mounts — `shared` host directories via a machine share, or guest-local
  `disk` directories — default env + env allowlist). Exec is the hot path:
  machine- or space-level `run` (collected) and `exec` (NDJSON stream with
  piped stdin, tty + resize, signals), host→guest cwd translation through the
  space mounts (`hostWorkdir`, one rule: `protocol.MapHostPath`). Lifecycle,
  ports, disks, egress (deny by default), host-substituted secrets, full
  snapshots, fork and warm pools stay as optional capabilities; a remote
  machine is just another implementation (`runtime: "endpoint"`).
- **Component kind** `bnbloc.sandbox.package/v1` / contract
  `bnbloc.sandbox.contract/v1` (pkgmeta validation, extreg/extpipe installer,
  gateway catalog + artifact entitlement, desktop/web tolerate the kind). The
  shared sidecar launch block gained `tokenEnv`, `dataDirEnv` and `files`
  (extra per-platform artifacts such as the VMM helper and firmware library,
  published as `artifacts["<platform>/<name>"]`).
- **Conformance suite** `sandbox/conformance` (`SANDBOX_URL`/`SANDBOX_TOKEN`).

Reference local implementation (separate repo, a libkrun sidecar embedding
the microsandbox 0.7.7 SDK) passes the full suite on an Apple-silicon Mac:

| Operation | Measured |
|---|---|
| machine create (alpine, 512 MiB, disk + share + port) | ~0.4–0.6 s |
| space ensure (create / idempotent) | ~105–170 ms / ~26 ms |
| space exec `true`, client round trip (x100) | p50 0.9 ms, p95 1.2 ms |
| streamed exec, first stdout | ~25–35 ms |
| pause / resume | ~4 ms / ~2 ms |
| stop / cold start (spaces re-established) | ~0.12–0.19 s / ~0.2 s |
| full snapshot (512 MiB guest) | ~0.70–0.77 s (~65–70 MB) |
| restore with copy-on-write memory | ~0.19–0.30 s |
| fork (live) | ~0.41–0.47 s |
| warm pool: prewarm 2 / claim | ~0.4 s / ~1 ms |

Open issues found while building it:
- Full-snapshot restore of a machine with secret bindings fails in the
  runtime (the TLS-interception device state cannot be restored), so the
  sidecar refuses such snapshots with `capability_missing`; pools must be
  built from secret-free snapshots until upstream fixes it.
- Shared mounts: the runtime presents host-created files as root-owned in the
  guest; a space ensure hands the room subtree to the space user (virtualized
  ownership, host untouched) but entries the host creates later are not
  writable by the space until the next ensure. Idmapped bind mounts (or one
  share per room with a uid presentation) would remove this; until then the
  bridge re-ensures per turn.
- Per-space egress (`spaceEgress`) is not offered: the runtime enforces egress
  per VM on the host; a per-user rule would live inside the guest.
- Secret values passed per create are stored by the runtime in its private
  data dir (0700) for the machine's lifetime.
- The runtime derives Unix socket paths from its home and macOS caps them at
  104 bytes; the sidecar hands it a short verified link under /tmp, since a
  desktop component data dir is too long.

## Phase 1 contracts (2026-10-09)

**Desktop → bridge** (env at bridge launch, memory only):
`BNBLOC_SANDBOX_URL`, `BNBLOC_SANDBOX_TOKEN` (the sidecar's admin bearer; never
leaves the bridge), `BNBLOC_SANDBOX_VMEXEC` (absolute path of the
`bnbloc-vmexec` helper shipped in the sandbox package), `BNBLOC_SANDBOX_MODE`
(`off` | `borrowed` | `all` — member setting; tenant policy may raise it),
`BNBLOC_SANDBOX_MACHINE` (the name of the one machine the desktop prepared),
`BNBLOC_SANDBOX_WORKSPACES_ROOT` (the host directory shared into that
machine). `BNBLOC_SANDBOX_MODE` is always set; the other five only while the
sandbox is ready (sidecar healthy AND the machine running) and the mode is not
`off`. `bnbloc-vmexec` is the declared sidecar file of that exact name.

**Machine ownership** (decided 2026-10-09): the desktop owns the single
machine. Once the sidecar answers discover it creates (or starts) the machine
named `bnbloc` — the prewarm — and only then hands the envs above to the
bridge (stabilizing restart; again when the sandbox stops or the mode
changes). The bridge never creates, stops or deletes machines; it ensures
spaces, mints exec tokens and runs commands on `BNBLOC_SANDBOX_MACHINE`.
Mode `off` deletes the machine (no VM runs) and hands only the mode.
- **Guest profile**: the sandbox package declares an optional sandbox-only
  `guest: {image, cpus?, memoryMiB?}` block (pkgmeta-validated, carried in the
  member catalog) — the image is the publisher's, never hardcoded in the
  desktop. A package without it runs its sidecar but is not prewarmed (status
  "no guest image declared").
- **Shares**: never the member's home directory (it would expose ~/.ssh,
  keychains, credentials and every other project to the VM and to borrowed
  prompts). The desktop creates one dedicated workspaces root,
  `<home>/bnbloc-workspaces`, and shares it (share `workspaces`) at the
  identical guest path; sandboxed room workspaces live under it and space
  mounts sit under it. Nothing else of the home directory is shared.
- **Egress (phase 1)**: the machine is created with egress mode `public`
  (public internet; private ranges, LAN, host loopback and link-local /
  metadata stay blocked) when the sidecar advertises `egressPolicy`, else the
  protocol default (`none`). Open item: tenant-admin-managed egress rules
  (allowlisted private hosts) as an explicit policy input.

**Space-scoped exec tokens** (Sandbox Protocol addition): the bridge mints a
short-lived token valid only for run/exec in one space as that space's user
(`POST .../spaces/{space}/exec-tokens` → {token, expiresAtMs}). The admin
token never reaches a core or an agent process; an agent that reads its own
environment can only run commands in its own room's space.
Exact rules (implemented in `sandbox/protocol`, prose in
`docs/architecture/sandbox-protocol.md`, checked by the conformance step
`execTokens`): optional capability `execTokens`; `POST
/sandbox/v1/machines/{machine}/spaces/{space}/exec-tokens` `{ttlSeconds}` (0 =
implementation default; the answer's `expiresAtMs` is authoritative) with the
ADMIN bearer → `{token, expiresAtMs}`. An exec token authorizes only `POST
.../spaces/{space}/run|exec` of that space and the exec control endpoints
(`.../execs/{execId}/stdin|resize|signal`) of execs started with the same
token (piped stdin and Ctrl-C need them); every other route answers 403
`forbidden` (incl. minting), an expired/unknown token 401. Deleting the space
revokes its tokens; a re-ensure keeps them.

**Bridge → core** (Core Protocol optional capability `sandboxExec`, advertised
by the core's discover): session create / turn requests carry
`execution: {mode: "sandbox", sandbox: {url, execToken, space, vmexec,
mounts: [{host, guest}], passthrough: [command names], hostConfine: {workspace,
allowRead: [...], allowWrite: [...]}}}` — absent = run on the host as today.
Final wire shape (`core/protocol/types.go` `Execution` / `SandboxExecution`,
top-level `execution` on `CreateSessionRequest` AND `SendInputRequest`; in
process `core.SessionOptions.Execution`): `{mode:"sandbox", sandbox:{url,
execToken, expiresAtMs, machine, space, vmexec, workdir, mounts:[{host,guest}],
passthrough, hostConfine:{workspace, allowRead, allowWrite, denyWrite}}}`
(`denyWrite`: paths that stay read-only even inside an allowed tree — e.g. the
routing config a core places in the workdir; it wins over every allow). Added vs the
first draft: `machine` (exec routes are addressed by machine + space),
`workdir` (the host directory under `BNBLOC_SANDBOX_WORKSPACES_ROOT` the core
MUST run this session's CLI in — the bridge picks `<root>/<space>`, mounts it
at the identical guest path, and `hostConfine.workspace` is the same dir) and
`expiresAtMs` (token expiry). A fresh token arrives with every turn; the bridge
never mixes sandboxed and host turns in one session (the session key carries
the execution kind), so a session's workdir never moves.
Workspace placement (decided 2026-10-09): the bridge writes `.room/` context
and attachments into the workspace BEFORE the session starts, so a sandboxed
session's WORKSPACE is created in the workdir: `CreateWorkspaceRequest` gained
optional `workdir` (absolute host path under the workspaces root; in process
`core.WorkdirWorkspaceCreator.CreateWorkspaceAt`, offered through
`core.As(..., sandboxExec)`), sent only to `sandboxExec` cores for a session
that will be sandboxed. Sandboxed create/input must carry
`execution.sandbox.workdir` equal to that workspace's dir (`bad_request`
otherwise). When an agent's execution kind changes (mode change, sandbox
became ready/unavailable, borrowed vs own) the bridge uses a separate
session + workspace for the new kind (key `room, core, provider, exec`) —
never rebinds or moves files. A core must refuse
(`bad_request`) an execution it cannot honor rather than run on the host;
`CreateSessionRequest.Core` / `SendInputRequest.CoreOptions` return that
error. The core hands the descriptor to vmexec/the CLI env with
`protocol.VMExecEnv` (env `BNBLOC_VMEXEC_URL|TOKEN|MACHINE|SPACE|PATH|WORKDIR|
MOUNTS|PASSTHROUGH`, `BNBLOC_CONFINE_WORKSPACE|ALLOW_READ|ALLOW_WRITE`);
vmexec parses it with `protocol.SandboxFromEnv` — one encoding.
Policy (must not break borrowing for members without the VM component):
- borrowed drives run sandboxed whenever the owner's machine has a ready
  sandbox and the core supports `sandboxExec` (member mode `borrowed`, the
  default, or `all`);
- they are REFUSED (fail closed, visible job notice) only when the sandbox
  or the capability is missing and the tenant policy
  `requireSandboxForBorrow` is on (org admin switch, default off) or cannot
  be read (decided 2026-10-12, remediation plan O2: an unreadable
  requirement is held to, not skipped; this applies to borrowed and
  platform drives alike);
- own drives follow the member setting (`all` = sandboxed, otherwise host).

**Core responsibilities** (per CLI): route shell commands through `vmexec`
(Claude Code `CLAUDE_CODE_SHELL_PREFIX` router; Codex `codex exec-server` in the
guest + `CODEX_EXEC_SERVER_URL`; native harnesses / opencode via their
command-rewriting hooks), keep file/MCP/hooks on the host, confine the CLI
process with the host sandbox profile `vmexec` generates (Seatbelt on macOS),
allow installed plugin/skill dirs, and pass through only `passthrough`
commands to the host.

**`bnbloc-vmexec`** (public, generic Go helper): `vmexec run -- <cmd>` (exec in
the space with host→guest cwd mapping, faithful stdio/exit code),
`vmexec claude-prefix` (the Claude Code prefix router), `vmexec confine --
<cli…>` (generate + apply the host sandbox profile), `vmexec hook <harness>`
(command-rewriting hook adapters).

## Phase 1 implementation notes: desktop (P2, 2026-10-09)

Landed in `apps/desktop/src-tauri/src/internal_ext/` (`sandbox.rs`,
`sandbox_api.rs`, `sandbox_bundle.rs`) plus small hooks in the generic sidecar
manager. The package gained an optional `guest` block (pkgmeta, extpipe
`PackageConfig`, member catalog).
- **Selection**: sandbox components with runtime `sidecar` are supervised. If
  several are installed, the lexically first name runs and the others report
  `inactive` (they stay discovered, so they are not treated as uninstalled).
- **Bundle**: the executable plus every declared file are downloaded through
  the one verified download path (`fetch_verified`: gateway origin only,
  sha256-pinned, temp file + rename). Layout:
  `<app cache>/sandbox/<name>/<version>-<id>/` with exactly the declared file
  names. The id hashes every (file, sha256) pair, so a republished helper gets
  a fresh dir. The bundle is assembled in a hidden staging dir and renamed into
  place; older bundles are removed. Uninstalling removes the bundles and the
  data dir.
- **Launch**: a fresh loopback port each launch, handed over in `listenEnv`
  (the package's fixed `localUrl` is ignored for sandboxes). A fresh 256-bit
  bearer goes in `tokenEnv` (a redacting `Secret` type; never persisted or
  logged). The data dir is `<app data>/sandbox/<name>` (0700). It also gets
  `supervisedEnv=1` and each declared file's path in its env. A sandbox
  sidecar left running by an earlier app session is identified by spawn
  record + executable path under the bundle root, and stopped before the new
  launch.
- **Health**: discover is polled for up to 60 s. Contract, protocol 1.x and
  `exec`/`spaces`/`shares` are required. On timeout the child is killed and
  the supervise loop restarts it with backoff. A machine watch runs every
  15 s and re-prewarms a lost machine, at most once a minute.
- **Prewarm**: pull the image (`POST /images`), then create the machine as
  recorded above. An existing machine `bnbloc` is reused (running), resumed
  (paused), started (stopped), or deleted and recreated (failed). "Restart VM"
  runs stop + start and recreates the machine if that fails. A restart keeps
  the bridge handoff: same URL, token and machine.
- **Mode**: `Settings.sandboxMode` (`borrowed` default | `all` | `off`) lives in
  the desktop settings file. Commands: `sandbox_status`, `sandbox_mode_get`,
  `sandbox_mode_set`, `sandbox_restart_vm`. The bridge-stabilizing watcher
  carries a token-free fingerprint (mode, launch seq, URL, machine, vmexec)
  beside the core set, so a handoff that appears or disappears, or a mode
  change while ready, restarts the bridge on the existing path.
- **Web**: Settings → "Agent sandbox" (desktop shell only; hidden in a
  browser). It has a mode selector, a status line (not installed / starting /
  ready with boot time / off / error) and "Restart VM".
- **Gaps / follow-ups**: the reference package needs a `guest` block and a
  `bnbloc-vmexec` file; until then the desktop reports "no guest image
  declared" / "ships no bnbloc-vmexec helper" and withholds the handoff. Only
  darwin-arm64 artifacts exist today. Tenant-admin egress rules are still
  open (see above).
- **Verified against the real runtime** (reference sidecar + its runtime
  files, Apple silicon; `sandbox_live_tests.rs`, `--ignored`): the catalog
  entry becomes a spec; the bundle is laid out from a local artifact server
  and verified; launch env carries a fresh bearer. Measured: discover 2.5 s
  after spawn; first prewarm with a fresh data dir 114 s (alpine image pull
  108 s, first create 6.3 s); VM restart (stop + start) 0.8 s. Delete is
  idempotent, a wrong bearer gets `unauthorized`, and the sidecar exits on
  stdin EOF. The image pull is a first-launch cost: the desktop's data dir is
  persistent, so later launches reuse the cached image.

## Phase 1 implementation: bridge, vmexec, tenant policy (2026-10-09)

Landed in this repo (public, generic):

- **Sandbox Protocol** — optional capability `execTokens`: `POST
  .../spaces/{space}/exec-tokens` (admin bearer) → `{token, expiresAtMs}`; the
  token authorizes only that space's run/exec and the control endpoints of
  execs it started, everything else 403, expired/unknown 401, revoked by space
  deletion. `protocol.Client.MintExecToken`; conformance step `execTokens`.
- **Core Protocol** — capability `sandboxExec`: `execution` on session create
  and input, `CreateWorkspaceRequest.workdir`, `core.WorkdirWorkspaceCreator`,
  `core.Supports`; `protocol.VMExecEnv` / `SandboxFromEnv` are the one
  descriptor ↔ environment encoding; `protocol.VMExecForwardEnv` is the env
  allowlist both vmexec and the space use.
- **`cmd/bnbloc-vmexec`** —
  `run [--stdio] -- <string | argv…>` (space exec over the exec token; cwd
  mapped host→guest with `MapHostPath`, refused with exit 126 outside the
  mounts; byte-exact streamed stdio, piped stdin, SIGINT/TERM/HUP forwarded,
  exact exit code, 125 for its own failures; passthrough names run on the host
  only as a bare program or a metacharacter-free simple command);
  `claude-prefix` (also `--claude-prefix` and argv0
  `bnbloc-vmexec-claude-prefix`; Claude Code does not word-split the prefix):
  only Bash-tool strings go to the guest — the host shell snapshot is dropped
  and the cwd-tracking `pwd -P` is redirected to a file in the space user's
  guest home (the space's own writable state), read back and removed after the
  command, mapped guest→host and written to Claude's own cwd file — so nothing
  needs to be shared or writable on the host for it (the spike's
  `mv … Operation not permitted` is gone); hooks / MCP servers / status line
  run on the host. `confine [--harness H]… [--allow-read|--allow-write|
  --allow-write-prefix|--deny-read|--deny-write P]… [--deny-keychain]
  [--print-profile] -- <cli…>` (`--allow-write-prefix` = temp-then-rename
  files beside a read-only config dir, e.g. `<dir>/.state-`; Seatbelt regex,
  bubblewrap covers only files existing at start):
  Seatbelt (`sandbox-exec -p`) on macOS, bubblewrap on Linux, refuses
  elsewhere; exec, no fork. Reads: deny content of credentials, personal
  folders, every visible home directory (other projects), the desktop app's
  data, other room workspaces and unlisted harnesses; re-allow workspace,
  CLI install dir, listed harness trees, installed plugin/skill sources (from
  Claude's `installed_plugins.json` / `known_marketplaces.json` and symlinked
  skills/commands/agents), vmexec, the Go module cache, allowRead. Writes:
  deny by default; workspace, temp, allowWrite and the listed harnesses'
  explicit STATE dirs/files only; config that executes on the host later
  (settings, hooks, plugins, skills, agents, commands, MCP config incl.
  `~/.claude.json`, plugin package caches) stays read-only; denyWrite last.
  A deny-write path is protected before it exists (a `denyWrite` entry
  ending in `/` names a directory; the bridge sends the workspace's
  protected paths that way, `core.ProtectedWorkspacePaths`: `.room/`, the
  agent config the session wears and the host-executed project config
  `.claude/`, `CLAUDE.local.md`, `.codex/`, `opencode.json(c)`, 2026-10-12):
  Seatbelt denies creating it; bubblewrap can
  only bind an existing path, so confine first creates each missing
  deny-write path inside a writable tree as an empty placeholder (a
  directory, an empty file, `{}` for a `.json` file) and binds it read-only,
  refusing the run if it cannot. A mount moves with its parent, so the
  directories between the writable tree and a deny-write path (e.g.
  `<workspace>/.claude` for `.claude/skills/`) are bound onto themselves,
  still writable, which makes them unrenamable mount points; Seatbelt rules
  match paths and need neither step (2026-10-12; a borrowed agent can no
  longer create `CLAUDE.md` or `.claude/skills/` in its workspace on Linux,
  nor rename `.claude` away to recreate it).
  `hook claude` (PreToolUse dialect, snake/camel keys) and `hook opencode`
  (`{tool,args}` → `{args}`) rewrite shell commands to `'<vmexec>' run --
  '<cmd>'`, leaving passthrough commands alone. Only an exact
  `<vmexec> run [--stdio] -- <word>` counts as already routed (a command
  that merely starts with the vmexec path is wrapped too), and an event the
  adapter cannot read exits 2, which blocks the tool in the PreToolUse
  contract (other non-zero codes would let it run unrewritten on the host;
  2026-10-12). Reads also deny `~/.bnbloc` (the CLI's sign-in and publisher
  key) and the desktop's web storage beside its data dirs. Verified on macOS with real
  `claude -p` turns under `confine` (incl. a Write tool); the integration test
  (`SANDBOX_URL`) drives the binary against a live sidecar.
- **Bridge** — `BNBLOC_SANDBOX_*` read once, token unset from the process env,
  config redacted in logs. Prewarm/readiness probe at start and every roster
  tick (discover with spaces+shares+execTokens, machine running). Policy per
  drive exactly as the contract; refusal happens before any borrow quota is
  reserved, posts a `sandbox-required` notice and fails the job with
  `sandbox_required`; an unreadable tenant policy (no answer cached within 30 s) refuses the same way, fail closed (2026-10-12; it used to fall back to "not required"). Per
  sandboxed turn under the drive lock: workdir `<root>/<space>` (0700),
  space re-ensure (shared mount at the identical path, env allowlist, labels),
  4 h exec token; sessions/locks keyed by execution kind. Roster: `&sandboxed=`
  on the room subscription → realtime `sandboxed` → shield badge in the web
  picker and members dialog.
- **Tenant policy** — `tenants.require_sandbox_for_borrow` (migration
  `2026_10_sandbox_require_for_borrow`), `PUT /v1/tenant/settings
  {requireSandboxForBorrow}` (manage_org; Organization → Settings switch),
  read-only for members through `GET /v1/my/tenant-policy[?tenant=]`
  (membership of the named tenant checked; it replaced the original
  `/v1/my/sandbox-policy` read in
  `2026-10-09-artifact-shares-and-site-previews.md`); the web Settings
  sandbox section shows the requirement.

Open items: an owner-managed passthrough allowlist (the bridge sends none
yet, so borrowed and own drives run every command in the guest); guest-disk
overlays for heavy generated dirs (node_modules, target, .venv) are not
mounted — they would hide those dirs from host-side language servers and MCP
tools; per-member deny roots beyond the visible home directories.
