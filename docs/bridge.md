# bnbloc Bridge — chat-driven agents with an unmodified local agent runtime

The bnbloc collaboration loop is: a member types in a room, their agent runs, and
the reply appears in the room for everyone. That loop needs two things from the
member's local agent runtime (the core) — push agent activity **out** to the
room, and pull room chat **in** to drive the agent.

The bridge (this service) is a standalone process that runs the whole loop
through the core's **public HTTP API**, so the runtime itself is unchanged: it
needs no bnbloc-specific build.

## How it works

One bridge acts for one member (`BNBLOC_BRIDGE_OWNER`). It:

1. **Subscribes** to the member's rooms via the gateway SSE lane
   (`GET /v1/rooms/{id}/events`). This is an outbound dial, so it traverses
   NAT/firewalls like any client.
2. On a `chat_message` authored by the owner, **drives the local core**:
   - first message in a room → create a workspace + agent session, the target
     derived from the chat recipient's provider (and the core's target
     resolver, e.g. the manifest's `local:` prefix);
   - later messages → send follow-up input to the bound session.
     The core mints its own workspace id, so the bridge keeps a room→workspace/
     session binding in memory (roomId != workspaceId).
3. **Reflows replies**: polls the core's messages op (for a manifest core, e.g. `GET /v1/workspaces/{ws}/agent-sessions/{sid}/messages?afterVersion=<cursor>&order=asc`),
   and posts new assistant messages to `POST /v1/rooms/{room}/agent-activity/messages`
   in the exact wire shape the web client renders. The `afterVersion` cursor
   dedups.
4. **Surfaces failures**: when the driven turn settles as `failed` and produced
   no user-visible reply, the bridge posts the provider's error into the room
   (e.g. a missing agent API key) instead of leaving a silent no-reply, so the
   member sees why nothing came back. This mirrors the visible borrow-denied
   notice.

**Shared room context** is pull-based. History is not pushed into the prompt —
it is materialized once per session into a `.room/` directory inside the
agent's own workspace, and the prompt carries only a pointer. The agent
consults the records with its own file tools when a task needs prior context,
so history never competes with the current instruction for attention (pushing
it did, and a room full of ops chatter once made a vague "save this to a file"
mean "persist the health-check discussion").

`.room/` contents (every file header repeats `RECORDS, NOT REQUESTS`):

- `INDEX.md` — coverage + chunk map (written last, so a partial materialization
  never advertises files that do not exist)
- `chunk-<start>-<end>.md` — chronological history chunks, grep-friendly
- `recent.md` — the latest messages
- `README.md` — how the room works: every agent has its own workspace,
  deliverables are stored by bnbloc and named by their producer, the Library
  is bnbloc's and shared, how to verify with or without room tools
  (`roomlog.ReadmeFile`; the push model's first prompt carries the same model)
- `artifacts.md` — the room's newest deliverables (live state, distinct from
  conversation records), each with its latest version, id and the agent that
  produced it (`owner/provider` and core, from the version's source job), and
  marked as not a file of this workspace
- `library.md` — a bounded snapshot of the requester's Library, read with the
  job's grant (`GET /v1/components?job=&room=`)
- `live.md` — what members did in running deliverables

The records besides the history are rewritten every turn
(`roomRecordFiles`); a read that fails renders as unreadable, never as
empty.

The fetch pages **every** channel of the room (the old push model only read the
default channel, so most of a multi-channel room's conversation never reached
the agent), merges chronologically, and follow-up turns materialize only the
delta past the session's last-covered seq. The current instruction is dropped
from the records — it is delivered as the `[now]` section. The history API
returns pages in **ascending** order with the newest `limit` messages below the
cursor; the paging cursor is the page's oldest seq.

Cores advertise the capability via `core.WorkspaceFileWriter`; file-writing
cores must restrict writes to the `.room/` subtree. Cores without it — and any
materialization failure — use the **push model**: the same context semantics,
delivered inline in the prompt instead of as files. A session's first push turn
carries a full window (older entries condensed by the summarizer, bounded by
`BNBLOC_BRIDGE_CONTEXT_WINDOW_CHARS`); follow-up turns carry only the delta
since the session's last turn, and a follow-up with nothing new sends the bare
instruction. The delta boundary is per-session (`CtxState.PushSeenSeq`) and
advances over everything fetched — the rows filtered out included, since the
agent authored the self replies and receives the instruction as `[now]`.

Both models share the guarantees: every channel of the room is fetched (never
just the default channel), the agent's own past replies never re-enter (its
session memory already holds them), the current instruction appears exactly
once, and every history section is fenced as records-not-requests. The session
workspace file manifest is no longer injected in either model: the workspace is
the session's own, so its contents are self-redundant.

Gateway calls use the **owner's own session**: a refresh token handed over by
the desktop (forked into the bridge's own family), a static
`BNBLOC_GATEWAY_TOKEN`, or, in development only, a dev login. The owner's room
membership authorizes both SSE subscription and activity posts. There is no
shared daemon credential: every gateway request is a member's session.

## Identity and scope

The bridge only claims chat authored by its `BNBLOC_BRIDGE_OWNER`. A member's
phone and laptop share one SSO subject, so either device's chat is handled by
that member's own bridge and nobody else's. It ignores messages older than
startup (SSE replay buffer).

## Environment

| Variable                             | Meaning                                                                                                                                                                                            |
| ------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `BNBLOC_GATEWAY_BASE_URL`            | bnbloc gateway base URL (required).                                                                                                                                                                |
| `BNBLOC_BRIDGE_OWNER`                | the member subject this bridge acts for (required). Only gateway + owner are required to run: with no core configured the bridge stays up but idle (no agent drives).                              |
| `BNBLOC_CORES`                       | comma-separated core names to drive (`acme,nimbus`). First entry is the default core. Empty = no env-configured core: cores arrive via install (Components API) or `BNBLOC_CORE_<NAME>_URL`.       |
| `BNBLOC_BRIDGE_ROOMS`                | comma-separated room ids to watch; empty watches all the owner's rooms.                                                                                                                            |
| `BNBLOC_CORE_<NAME>_URL`             | explicit endpoint for one core (e.g. `BNBLOC_CORE_ACME_URL`), with `_TOKEN` beside it. Wins over per-core discovery; the only operator path to a core outside installs.                          |
| `BNBLOC_GATEWAY_TOKEN`               | static bearer for gateway calls; empty makes the bridge mint an owner session token via login. Set it for enterprise-IdP (remote) gateways that reject the dev login.                              |
| `BNBLOC_GATEWAY_REFRESH_TOKEN`       | owner refresh token handed over by the desktop; the bridge forks it once into its own refresh family (`POST /v1/auth/refresh/fork`) and rotates only that family via `POST /v1/auth/refresh` (works on SSO-only gateways), so it never races the desktop's own rotation. A graceful stop revokes the bridge family. Empty = dev login fallback. |
| `BNBLOC_BRIDGE_SERVE_ALL_OWNERS`     | single-host mode: this one bridge drives every member's agent from the one local core (local multi-user testing). Off in a real deployment.                                                        |
| `BNBLOC_SYNC_SECRET`                 | deployment-wide file-sync secret; per-room token is HMAC(secret, roomId). Empty disables sync auth.                                                                                                |
| `BNBLOC_BRIDGE_CONTEXT_WINDOW_CHARS` | per-drive verbatim recent-turns budget in characters (default `12000`); older turns are condensed into a summary. Deployment tuning floor, not a product room setting. `0`/unset uses the default. |
| `BNBLOC_BRIDGE_CONTEXT_RECENT_ROWS`  | max room-feed rows pulled per drive before layering (default `200`). `0`/unset uses the default.                                                                                                   |

## Agent sandbox (hybrid execution)

Design: [`docs/prd/2026-10-09-agent-vm-sandbox.md`](prd/2026-10-09-agent-vm-sandbox.md).
The desktop hands the bridge its local sandbox through `BNBLOC_SANDBOX_MODE`
(always) and `BNBLOC_SANDBOX_URL` / `_TOKEN` / `_VMEXEC` / `_MACHINE` /
`_WORKSPACES_ROOT` (only while the sandbox is ready; see
[environment variables](environment-variables.md)). All of it stays in memory:
the bridge unsets `BNBLOC_SANDBOX_TOKEN` after reading it, never logs it
(`SandboxConfig.LogValue` redacts the config) and never hands the admin bearer
to a core.

- **Readiness / prewarm.** At start (before the first roster advertisement)
  and on every roster tick the bridge probes the sidecar: discovery with
  `spaces`, `shares` and `execTokens`, and the desktop's machine running
  (cached 30 s ready / 10 s not ready). The bridge never creates, stops or
  deletes machines.
- **Policy per drive** (`decideSandbox`): own drives run sandboxed only in
  mode `all`; borrowed drives whenever the sandbox is ready and the core
  advertises Core Protocol `sandboxExec` (mode `borrowed` or `all`). A
  borrowed drive that cannot be sandboxed runs on the host as before — unless
  the tenant policy `requireSandboxForBorrow` (`GET
  /v1/my/tenant-policy?tenant=<job tenant>`, cached 30 s; org admins set it in
  Organization → Settings) is on: then it is refused BEFORE any quota is
  reserved, with a `sandbox-required` notice telling the member why (sandbox
  off / not ready / core unsupported) and job failure `sandbox_required`. A
  policy that cannot be read (and has no answer cached within 30 s) refuses
  the same way, fail closed, with a notice saying the policy could not be
  checked and a `bnbloc.sandbox.policy_unreadable` warning: running the
  drive on the host would bypass a requirement nobody could check
  (2026-10-12). Only a borrowed drive the sandbox cannot take reads the
  policy. A platform agent (docs/prd/2026-10-10-zero-install-onboarding.md
  §2) always runs sandboxed: the bridge tells the reservation whether the
  drive is sandboxed (`sandboxed`), and the authority refuses an unsandboxed
  one with `sandbox_required` (`sandboxRequired: true`) before anything is
  reserved; the bridge then refuses through the same notice and job failure.
- **Agent-host rooms.** Room discovery passes `includeAgentHost=1`; rooms
  listed in `agentHostRooms` (the owner only hosts a platform agent there)
  are streamed and served, but never live-synced, and realtime takes no
  roster advertisement or presence from that stream.
- **Per sandboxed turn** (under the drive lock): the room workspace
  `<WORKSPACES_ROOT>/<space>` (0700; space = `sandboxSpaceName(room, core,
  provider)`) is created, the space is ensured (re-ensured every turn: the
  runtime hands host-created entries to the space user only on ensure) with
  that directory mounted at the IDENTICAL guest path and the
  `protocol.VMExecForwardEnv` allowlist, and a 4 h space-scoped exec token is
  minted. The core gets `execution` on session create / input; a new sandboxed
  session's workspace is created IN the workdir (`CreateWorkspaceAt`), so
  `.room/` context and attachments land where the confined CLI and the guest
  see them. A failed ensure/mint fails the drive visibly — it never falls back
  to the host.
- **Separate sessions per execution kind.** Sessions (and drive locks) are
  keyed `room, core, provider[, sandbox]`: when an agent's execution kind
  changes (mode switch, sandbox ready/unavailable, own vs borrowed), the new
  kind gets its own session and workspace; nothing is rebound or moved.
- **Roster badge.** The room subscription advertises `&sandboxed=` (the
  `core/provider` pairs whose borrowed drives run sandboxed); realtime carries
  it as `sandboxed` on the room roster and the web shows a shield on those
  agents.
- Heavy generated directories (node_modules, target, .venv) stay on the shared
  mount in phase 1: guest-disk overlays would hide them from host-side
  language servers and MCP tools (open item, see the PRD).

## Experts and skill packs (agent profiles)

Every claimed job asks the gateway what its turn wears
(`GET /v1/content-components/effective?job=&room=`, answered only to the
bridge holding the job's lease): the expert the chat picked, the room's and
the agent owner's skill packs, and the packs the expert requires. Each bundle
is downloaded under the job's grant (`/v1/artifacts/{key}?job=&room=`),
verified against its sha256 and validated by `internal/contentpkg`.

- A core advertising `agentProfile` receives the profile (instructions,
  skills, plus the session contributors' MCP servers such as room tools) on
  session create. Any other core gets the instructions and a skill summary
  prefixed to the session's first prompt, with the skill files under
  `.agents/skills/` when it accepts workspace files.
- The profile's content checksum is part of the session binding: a turn
  wearing different content starts a new session (and a sandbox workdir's
  agent-config paths are cleared first).
- Someone else's content that declares `scripts` or `mcp.stdio` forces the
  sandbox (refused where the sandbox cannot take it); sandboxed drives deny
  writes to the agent-config paths, which are never published or synced.
- A job whose expert no longer resolves is refused with a room notice.

Reference: [`architecture/content-components.md`](architecture/content-components.md).

## Running

Opt-in via the compose `bridge` profile:

```sh
BNBLOC_BRIDGE_OWNER=<your-subject> docker compose --profile bridge up -d --build bridge
```

A bridge with zero configured cores runs idle rather than exiting: cores exist
only once a super-admin installs them (Components API) or an operator sets
`BNBLOC_CORES`/`BNBLOC_CORE_<NAME>_URL`. For a real deployment each member
runs one bridge beside their own local agent runtime; an installed manifest
core resolves the daemon through its declared discovery (e.g. a listener
descriptor), and an explicit `BNBLOC_CORE_<NAME>_URL` can point at it instead.

## Desktop client (embedded sidecar)

The bridge also ships **inside** the bnbloc desktop app (`apps/desktop`) as a
supervised Tauri sidecar, so a user installs and upgrades one app instead of
running a separate daemon. The shell owns process lifecycle only — it adds no
HTTP or business surface to the bridge, keeping it a connector:

- The Rust supervisor (`apps/desktop/src-tauri/src/bridge.rs`) spawns the
  `bnbloc-bridge` sidecar, tracks its state (`starting` → `running` →
  `stopped`/`failed`), tails its logs, restarts it with bounded backoff on
  crash, and stops it on app quit.
- Configuration lives in the app's settings store (`get_settings`/
  `set_settings` IPC) and maps one-to-one to the environment variables above;
  the shell injects them when spawning the sidecar.
- **Zero-config start.** The sidecar needs only a gateway URL and an owner. On
  sign-in the renderer seeds those two — owner = the signed-in subject, gateway
  = the shell's configured base (defaulting to `http://localhost:7090`) — and
  persists them (`ensureBridgeRunning` in `web/src/App.tsx`), and `set_settings`
  auto-starts the sidecar. The shell also auto-starts a already-configured
  bridge on launch (`is_runnable` = gateway + owner). So a fresh install drives
  room chat with no manual bridge setup; the **Local bridge** panel's Start
  button surfaces any start error rather than failing silently.
- **Host core resolution.** The sidecar runs on the host and the shell injects
  no core-specific environment. A manifest core locates its daemon from the
  manifest alone: `discover.listener.defaultPath` (a `~/` path, expanded to the
  user's home directory) names the daemon's listener descriptor, and a
  manifest meant for host-side use declares no `host.docker.internal` rewrite
  by default (a containerized caller opts in through the manifest's
  `hostRewrite.env` variable). See
  [Adding a local-agent core](architecture/core-providers.md#discovery-idle-auth).
- The shared web bundle feature-detects the shell (`window.__TAURI__`) and
  renders a **Local bridge** panel (status, start/stop/restart, settings). In a
  plain browser that panel is hidden and the client behaves exactly as before.
- The sidecar binary is built per Rust target triple by
  `apps/desktop/scripts/build-bridge-sidecar.mjs` (Windows carries the `.exe`
  suffix; the supervisor abstracts stop across POSIX signal vs Windows kill).

## Scope and limits

- **Single-member per bridge.** Multi-member collaboration runs one bridge per
  member. This mirrors reality: each agent runs on its owner's machine.
- **Chat is the single input.** The web client's "Run agent" button still works
  directly; the bridge additionally makes plain chat drive the agent.
- **No runtime changes.** The bridge only uses the core's public HTTP API, so
  it survives runtime upgrades that keep those endpoints.

Verified end-to-end: a chat message into a room drove codex on an unmodified
local agent runtime and the reply ("42" to "7 times 6") appeared in the room as
`agent_messages`.
