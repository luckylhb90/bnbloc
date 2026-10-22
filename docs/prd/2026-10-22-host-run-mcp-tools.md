# Host-run MCP tools: a tool that holds a member's sign-in runs outside the agent's sandbox (2026-10-22)

Status: slices 1-4 built 2026-10-22 (not released); slice 5 (a tool adopts it)
and the QA run in a sandboxed session are open. Owner: the MCP tool package contract (`core/pkgmeta`), the
bridge (a tool host beside the room tools server), the Sandbox contract's
host confinement (`core/protocol`, `cmd/bnbloc-vmexec confine`).

A sandboxed agent session confines the agent CLI **and every process it
spawns**, its stdio MCP tools included. That is right for a tool that works
on the workspace and wrong for a tool that keeps a member's sign-in on their
machine: it cannot write its own state, and letting it write there would let
the agent write and read the sign-in too. This record moves such a tool out of
the agent's process tree, to the host, and lets the agent reach it only as an
MCP server.

## 1. Why

- QA 2026-10-22. A member's own agent in a sandboxed session (mode `all`)
  called a stdio tool that keeps a sign-in under the user's config directory.
  Every call, the status check included, answered
  `chmod <state dir>: operation not permitted`, so the agent could neither
  read nor sign in. The same tool, called by an agent whose core runs
  unsandboxed, worked. No upgrade was involved: the tool had never worked in
  a sandboxed session. Evidence: the room session's transcript (the tool's
  error, verbatim) and the tool's audit log, whose entries were written by the
  tool process under the unconfined core (checked by process ancestry and
  timestamps, not by tracing the sandbox).
- Why it fails: `cmd/bnbloc-vmexec/confine.go` states that the profile
  covers "the CLI and the hooks and MCP servers it spawns"; writes are denied
  except the workspace, temp dirs, the harness's own state and `allowWrite`.
  The tool's state directory is none of them.
- The obvious repair, `hostConfine.allowWrite` for the tool's state, is the
  wrong one. Reads are allow-by-default under the profile, so the agent can
  already read that directory; allowing writes as well makes the sign-in
  the agent's to read and to plant in. The tiers, scope and later-message
  confirmation that guard such a tool (the tool's own checks) then protect
  nothing against an agent that was talked into it.
- The sandbox design already says the intended shape: "MCP servers and
  credential-bearing plugins off by default for borrowers; opted-in servers
  are relayed from the host (their credentials stay on the host)"
  (docs/prd/2026-10-09-agent-vm-sandbox.md). The relay was not built; the
  room tools server (docs/architecture/room-tools.md) is the precedent for
  how: a loopback MCP server on the host with a per-session bearer, which
  the confined CLI reaches over the network the profile allows.

## 2. What it is (the end state)

### 2.1 Placement: a tool says where it runs

`mcp.stdio.placement` in the MCP package (stdio runtime only):

- `"agent"` (the default, today's behaviour): the tool is a child of the
  agent CLI. In a sandboxed session it is confined with the CLI. Right for a
  tool that works on the workspace with paths the agent chooses.
- `"host"`: the tool runs as a host process of the bridge, never as a child
  of the agent. Right for a tool whose job is to hold something the agent must
  not touch: a member's sign-in, a key, local state.

`mcp.stdio.privatePaths` (only with `"host"`): paths, relative to the user's
config directory, that the tool keeps its state in. They are what confinement
denies the agent's process tree to read (§2.4). Bounded and validated like the
other path fields (relative, no `..`, no absolute path, a few entries).

The author declares it; the package is signed and admin-approved like every
tool, so placement is part of what an admin approves. Nothing else changes in
a tool's contract: its settings, its room relay and its tools are the same.

It takes effect **in a sandboxed session, on the member's own drive**. Where
nothing confines the agent there is nothing to get out of, and the bridge does
not know every core's session directory (a tool that works in the session's
directory, as the agent's child does, needs it). On a drive for someone else the
declaration is ignored and the tool stays with the agent: an unconfined process
of the owner's must never serve a borrower.

### 2.2 The tool host

The bridge gets a **tool host** beside the room tools server, one place that:

- fronts a `"host"` tool for the session when the session is created (the
  profile names its endpoint), **starts it at its first request**, and stops it
  when the session binding is released or the bridge stops (the room tools
  release hook); one process per (session, tool), because the room relay's
  credential and the session's working directory are per session. A session
  restored after a bridge restart is fronted again from the same content;
- starts it through the existing launcher (`mcp-tool --dir --name`), so a
  tool update never changes the profile, with the environment it would have
  had as the agent's child (its settings, its settings file, the room relay)
  on top of the bridge's own environment **without the bridge's own settings
  and credentials** (`BNBLOC_*`), its working directory the session's
  (the sandbox workdir), never the agent's environment;
- serves it on the room tools server at `/tools/<name>`, with the session's
  bearer. The profile's MCP server for that tool is that URL (`http`) or, for
  a core that takes stdio only, the existing shim
  (`bnbloc-bridge roomtools-stdio --url …`), which needs no state access and
  so runs confined without harm;
- translates MCP Streamable HTTP to the tool's stdio. The host is the tool's
  one client: it initializes the tool once, answers the agent's own
  `initialize` itself (the server is already dual-era and mints no protocol
  sessions), gives each request a unique id towards the tool and routes the
  answer back by it, forwards progress notifications to the stream of the
  request that asked, and answers a request the tool makes of its client
  (sampling, roots) with an error: the agent's session is not the tool's.

The agent therefore sees an ordinary MCP server and cannot tell it from a
child. It can no longer reach the tool's process, environment or files.

### 2.3 Who may call, and when

- The bearer is the session's; it is bound to the job being driven each turn
  (the room relay's rule): between turns a call is refused (409), and a
  bearer that outlives its session is revoked with it.
- The binding is also dropped when the turn ends (`Unbind` at turn end, not only
  on close), so a bearer the agent kept cannot call between turns even before the
  session closes.
- A call reaches only a tool that was placed for **this session's** profile.
  A tool that could not be attached (needs setup, not downloaded, a drive the
  member did not ask for) is not started; it is reported as missing as today,
  never as a failed turn. Member settings are still never lent: a borrowed or
  platform drive never gets a `"host"` tool that carries them.
- A call has no fixed time limit (a slow sign-in, a long file transfer); it
  ends with the agent's tool call or when the tool's process dies.

### 2.4 Confinement of the state

A core that confines its agent CLI receives `denyRead` with the `privatePaths`
of every `"host"` tool of the session (a new field of `hostConfine`; `confine`
already takes `--deny-read`). The profile's deny of `file-read-data` on those
paths holds for the CLI and everything it spawns. The paths are tool
declarations resolved against the user's config directory by the bridge,
never an agent-supplied value. A core that does not confine receives nothing:
there is nothing to enforce, and the host placement still keeps the tool's
environment and process out of the agent's tree.

A core must pass the paths to `confine` as `--deny-read` flags, not only through
`BNBLOC_CONFINE_DENY_READ`: the helper a sandbox package ships can be older than
the variable, and the flag it has always had. A core that keeps its own copy of the
descriptor must carry the field too. Both were found on 2026-10-22 by asking a real
sandboxed agent to read a file in a tool's private directory: it could, through a
core whose descriptor copy lacked the field and then through a helper that did not
know the variable; with both fixed it gets `EPERM`.

Review of 2026-10-22 changed three details of this section:

- **The deny is final.** `BNBLOC_CONFINE_DENY_READ_FINAL` (`DenyReadFinal`) makes
  the deny win over any allow that covers the path (the workspace allow, a
  parent directory); the soft `--deny-read` flag stays for helpers older than the
  variable. A real macOS profile test reads a denied file under a covering allow.
  On Linux (bwrap) the deny is a placeholder mount and is not built yet: the
  record claims the protection on macOS only.
- **The settings file is private.** The file that carries a host tool's placement
  into the session is written owner-only, in a directory the profile denies, and
  the content's binding checksum covers the placement (a changed placement is a
  changed binding).
- **What is shown and what is not.** A real sandboxed agent got `EPERM` on the
  tool's state files and could not reach the tool's process by path. Reading the
  tool's *environment* from the agent's side was tried and the control failed, so
  "the agent cannot read the tool's environment" is not claimed; the environment
  is kept small instead (`BNBLOC_*` stripped).

### 2.5 What does not change

- A tool placed `"agent"` is exactly as before, sandboxed or not.
- A session that is not sandboxed keeps a `"host"` tool as the agent's child,
  exactly as before: nothing confines it there, and the bridge cannot name
  every core's session directory.
- The tool's own rules (tiers, scope, who may confirm) stay in the tool. The
  placement only decides who can reach its process and files.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member (their own drives only); the agent (calls the tool as an MCP server, nothing more); a borrower or platform drive (never gets a `"host"` tool carrying member settings: not attached); a core that confines its CLI (receives `denyRead`); an admin (approves the package, so placement). Another local process holding no bearer is refused. |
| Time and lifecycle | The tool lives with the session binding: started with it, stopped on release, rebind, forgotten room, core change and bridge stop. A turn between turns cannot call it (409). A crashed tool answers the call in flight with an error and is started again on the next call. A tool update takes effect at the next start (launcher), never changing the profile. |
| Scale and cost | One process per (session, tool), only for tools that declare `"host"`; none for a session that has none. One in-flight request map per tool. Reuse across sessions is refused on purpose (the relay credential and working directory are per session). |
| Failure and recovery | Tool cannot start (binary missing, setup unmet): reported missing on the job, as today. Tool dies mid-call: that call answers an MCP error naming the tool; the next call restarts it. Bridge restart: sessions are rebound and tools started again; a session restored without its tool says so. Nothing runs on the host silently when the host start fails. |
| Boundaries and privacy | The tool's state, key and environment are outside the agent's tree and (on a confining core) read-denied to it. The bearer is meaningless outside the bridge; the host process holds no gateway session. No value of a setting is logged or put in the profile checksum (the profile names the URL, not the tool's environment). |
| Abuse and untrusted input | The agent controls only the JSON-RPC it sends. It cannot pick a tool outside its profile, a path, an executable or an environment. A hostile tool answer is data like any tool output. The host process runs with the user's rights, so a tool declaring `"host"` is trusted code: that is why placement is a signed, admin-approved declaration and not a request. |
| One owner | The tool host is the one place that starts, serves and stops host tools; the validator for `placement` and `privatePaths` is one function in `core/pkgmeta`, used by the gateway's install and the bridge; the bearer, loopback server and release hook are the room tools server's, not copies. `denyRead` is built in one function from the placed tools. |
| Defaults and fallbacks | The default placement is `"agent"` (unchanged). A `"host"` tool outside a sandboxed own drive stays the agent's child (said above, tested). A tool that cannot be hosted never falls back silently to a place that hands the agent the state: an older bridge that does not know the field starts it as a child, which a confining core makes fail visibly; an agent that takes neither http nor the shim fails the contribution with a reason. A private path that cannot be placed (no config directory) refuses the confined drive rather than run it without the deny. No timeouts are invented. |
| External contracts | MCP Streamable HTTP and the stdio transport as the room tools server already speaks them (dual-era); `hostConfine` gains `denyRead` (additive, cores that ignore it keep confining as before; the confine helper already has `--deny-read`). The tool's own stdio server is unchanged. |
| Load on users and agents | None for agents: the same tools under the same names. For members: a tool placed `"host"` needs no setup of the sandbox. No new tool, no new prompt text. |
| Release and compatibility | Additive. Order: the validator and schema in the gateway first (a package with the field installs), then the bridge's tool host and the cores' `denyRead`, then the tool republishes with `"host"`. An older bridge ignores the field and behaves as today. Pre-launch, so no migration of old state. |
| Observability | The bridge logs tool start, stop and crash with the tool name and session (never values); a missing tool is reported on the job; the tool's own audit log stays with the tool; a counter of host-tool restarts per tool. |
| AI capability | None added; it removes a way an injected agent could reach a member's sign-in. |
| Component or platform | Platform: the contract field, the tool host and the confinement input are generic and name no product. The tools that use it live in their own repositories. |

**Pre-mortem.** It shipped and later failed. The most likely reasons:

1. *A host tool took a workspace path from the agent and read or wrote
   outside the workspace as the user.* The tool is trusted code by
   declaration; tools that act on paths the agent names stay `"agent"`. The
   package review (admin) is where `"host"` is granted, and the tool's own
   tests cover its path rules (a tool that sends files confines them to the
   session's directory, its working directory here).
2. *The HTTP-to-stdio translation mixed up two agents' answers.* One tool
   process serves one session, ids are mapped per request, and a test runs
   concurrent requests with answers returned out of order.
3. *A tool died and the agent hung.* A dead process fails every in-flight
   request at once; a test kills the tool mid-call.
4. *`denyRead` did not hold and the agent read the sign-in.* A test runs the
   real confine profile against a state directory and asserts the read fails
   for the CLI's child; the profile and the bridge's list come from one
   function.
5. *A tool updated and every session restarted.* The profile names the
   launcher and the tool's URL, never the binary path, as the launcher
   already ensures.

## 4. Build order (slices of §2)

1. Contract (done): `mcp.stdio.placement` and `privatePaths` in `core/pkgmeta`
   (validator on the one relative-path rule `core.CleanRelPath`) and in the
   job's tool list (`wire.ContentMCPTool`); a test per refusal.
2. Tool host (done): `services/bridge/hosttool` (the process, against a fake
   stdio MCP server: concurrent requests, out-of-order answers, progress, a
   request from the tool, a crash mid-call, a caller that leaves, close) and
   the endpoint on the room tools server at `/tools/<name>` with the
   session's bearer and the job binding (`roomtools/hosttools.go`).
3. Profile (done): in a sandboxed own drive a placed tool leaves the agent's
   profile and the contributor gives the agent its endpoint under the same
   name (http, or the stdio shim); the binding checksum is the content's, so
   it does not change with the sandbox. A real-process test covers
   environment, directory, relay and release; a restored session is fronted
   again.
4. Confinement (done): `denyRead` in `core.HostConfine`, the protocol's
   `hostConfine`, `BNBLOC_CONFINE_DENY_READ` and the confine helper; a test
   runs the real macOS profile and reads the denied file (and fails to write).
5. Adoption: a tool republishes with `"host"` and its `privatePaths`; QA in a
   sandboxed session (the failing case above) and in an unsandboxed one.
6. Later: a tool that serves HTTP natively needs no translation (placed
   `"host"` as an endpoint it already is); sharing one host process across a
   member's sessions if the per-session cost shows up.

## 5. Open questions

- Should `"host"` be required, rather than chosen, for a tool whose settings
  are member-scoped? It would remove a way to forget it; it would also stop
  a tool that wants the workspace and a member value.
- The agent CLI's own network allowance: the profile allows network today
  ("the CLI talks to its model API from the host"); if a future profile
  restricts loopback, the tool host's address must be allowed explicitly.
- Cores that do not confine their agent (the ACP core runs its agents on the
  host today): nothing here protects a tool's state from such an agent, which
  reads files as the same user. Confining those agents is its own record
  ([`2026-10-22-acp-sandbox.md`](2026-10-22-acp-sandbox.md)), after which their
  sandboxed sessions use host tools like any other.
- The session's working directory and temp directory are shared between the
  agent and a host tool by design; a link the agent leaves there is the tool's
  to refuse (a tool that saves downloads checks its download directory and re-checks a
  file's resolved path and content when a proposal is confirmed).
- A tool that works on the session's directory *and* holds a sign-in (a
  sender of files, say) runs host-side with the sandbox workdir as its
  directory, but can no longer see files the agent wrote outside it; the
  tool's own path rules are what stop it reaching further.
