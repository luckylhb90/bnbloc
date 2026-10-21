# What an agent installs in the sandbox stays; sign-ins never go through the chat (2026-10-21)

Status: slice 1 built (durable homes and per-user install places; the
reference passes the full conformance suite with the new `durableHomes`
step, and a home kept on a machine's disk before is copied in on its first
ensure, checked live).
Slice 2 (sign-ins) is designed here and not built. Owner: the Sandbox
Protocol (`sandbox/protocol`, `docs/architecture/sandbox-protocol.md`), the
reference implementation (the sandbox component's own repository); slice 2
also the sandbox's settings (`core/pkgmeta/sandbox_settings.go`), the
desktop's own machine and find-a-way.

A member's own agent, running in their agent sandbox, installs the tools a
task needs (a release CLI, a package manager) and signs in to them once.
Both are still there on the next turn, the next day and after the sandbox
machine is rebuilt. A credential is never typed into the room: the member
gives it to their sandbox, and the agent's commands use it without the
agent ever reading it.

## 1. Why

- QA 2026-10-21, a release room (member `maya`, own Claude Code agent, mode
  `all`): the agent had installed the release CLIs and signed them in. The
  next turn ran in her own sandbox machine and found neither ("not
  installed"). Twice that day she pasted a CI token into the room so the
  agent could sign in.
- What exists, checked against the code:
  - A space's home is `/home/sp-<space>` on the machine's own disk. A space
    deleted without purge keeps it (`spaceRestart`), but a machine deleted
    and created again starts with an empty one. The desktop recreates the
    own machine whenever its hosts change (`plan_own_machine`), and a
    sandbox update replaces the machines.
  - A space's uid is durable per space name across machines
    (`meta/space-uids.json`), and files a space creates in a share keep it
    (virtualized ownership). A home kept on the host would read back as the
    space's own.
  - `npm install -g` writes `/usr/local` (root-owned), so an agent cannot
    install a CLI the usual way. `PATH` has no per-user bin directory.
  - Credentials: `forwardEnv` lets named variables from the member's shell
    environment into their own sandboxed runs, so a member must edit a shell
    profile. The protocol's `secrets` (host-substituted: the guest sees a
    placeholder, the value is put in only on egress to listed hosts) is
    implemented by the reference and unused by the desktop.

## 2. What it is (the end state)

### 2.1 A space's home outlives its machine (slice 1)

- New capability `durableHomes`: a space's home survives the machine being
  deleted and created again, and is the same on every machine the space
  name is ensured on (as its uid is). Only a delete with `purge=1` removes
  it. A space name ensured on two machines at once shares one home.
- The reference keeps homes in its own data directory on the host
  (`<data>/homes/<space>`, owner-only), shared into each machine at a
  root-only path and bind-mounted at the space's home on ensure. They are
  never in a share the desktop declares, and never in the member's home.
- Caches stay fast and disposable: `~/.cache` and `~/.npm` are guest-local
  (on the machine's disk, gone with it). Everything else in the home is
  durable: installed tools, their configuration, shell history.
- Per-user install places: a space's commands get `PATH` with
  `~/.local/bin`, `~/.bun/bin`, `~/.cargo/bin` and `~/go/bin` first, and
  `NPM_CONFIG_PREFIX=~/.local` unless the run sets it (the reference sets
  both in its exec helper, which every space process starts through). So
  `npm install -g`, bun's installer, `pip install --user`, `cargo install`
  and `go install` put tools in the durable home, and the next command finds
  them.
- Conformance step `durableHomes`: write a file in a space's home and
  install a script into `~/.local/bin`; delete the machine; create another
  and ensure the same space. The file is there and the script runs by name.
  After a purge, the home is empty.

### 2.2 Sign-ins the member gives their sandbox (slice 2, designed)

- A **sign-in** is a name, the hosts it is for, and a value, set by the
  member for their own sandboxed runs. It lives in their member store on
  their computer, never on the gateway, never in the room.
- The desktop creates the member's own machine (`bnbloc-own`) with each
  sign-in as a protocol secret (`secrets [{name, value, hosts}]`). The
  guest's `$NAME` holds an opaque placeholder; the sandbox puts the value in
  only on egress to those hosts. A changed sign-in recreates the own machine
  when no own turn runs, as a changed host does; slice 1 keeps the homes.
  The shared machine, which runs borrowed and platform drives, never gets
  one.
- The agent uses it as a variable: `tool login --token "$CI_TOKEN"` stores
  the placeholder in the tool's config in the durable home. Every later
  call has it substituted. The agent never sees the value. Reading `$NAME`
  shows the placeholder, and a request elsewhere carries the placeholder.
- The AI asks; the member decides (find-a-way's `add-sign-in`). A reply that
  says a tool needs a token for a host offers a card: "give your sandbox a
  sign-in for `ci.internal.example`". It has a secret field, a name for it
  (`CI_TOKEN`, from the reply) and its hosts. Save stores it, applies it
  when idle and offers "Ask again". The value goes from the field to the
  member store and never through the gateway, an assist or the room.
- Shown in the sandbox's settings dialog as a list (name, hosts, set or
  not), with each value write-only.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member's own agent only (own machine, own spaces). Borrowed and platform drives run on the shared machine in spaces of their own seat: other homes, never a sign-in. |
| Time and lifecycle | A home lives until its room's workspace is deleted (the bridge purges the space with the room) or the sandbox's data dir is removed. A sign-in lives until the member removes it. A machine restored from a snapshot keeps homes as the snapshot had them (its device set is fixed). |
| Scale and cost | Homes grow with what agents install; caches stay guest-local. The desktop's sandbox card shows the data dir's size (follow-up if homes grow large). |
| Failure and recovery | A home that cannot be mounted fails the ensure (the drive fails `sandbox_starting`, retried): never an empty home silently. A sign-in the tool rejects is the tool's error, in the reply. |
| Boundaries and privacy | Homes are owner-only on the host and reachable in the guest only through the space's own mount. Sign-in values never leave the member's computer and never enter the guest. |
| Abuse and untrusted input | A guest can fill its home (disk use) and plant files in it. Nothing on the host reads or runs them: the host CLI and harness config live in the workspace (fenced), not the home. A placeholder sent to another host is useless there. |
| One owner | The protocol owns the capability; the reference owns where homes live; the desktop owns sign-ins and the own machine; find-a-way owns noticing. |
| Defaults and fallbacks | Without `durableHomes`, homes behave as today. No sign-ins means no secrets. The install places apply to every space and change only where tools go. |
| External contracts | Slice 2 relies on the runtime's TLS interception reaching the tool: a Node CLI trusts its own CA bundle unless `NODE_EXTRA_CA_CERTS` names the runtime's. Verify with the real CLI before building slice 2. |
| Load on users and agents | No new tool or rule for agents. They install and sign in as they would anywhere. Members see one card when a sign-in is missing. |
| Release and compatibility | Slice 1 is a sandbox component release (optin first). Existing homes on a running machine are moved into the durable place on their next ensure, so nothing installed is lost. |
| Observability | The reference logs where each home is mounted from. Slice 2: the desktop logs each sign-in applied (name and hosts, never the value). |
| AI capability | The agent finds and installs what it needs; find-a-way proposes the sign-in the reply asked for. |
| Component or platform | The capability and its conformance step are platform (a contract any sandbox implements); where homes live and how is the component's. |

**Pre-mortem.**
1. *An upgrade wipes what agents installed.* The first ensure after the
   upgrade copies the old guest-disk home into the durable one before
   mounting it. The live check installs a tool, upgrades and finds it.
2. *Builds get slow.* Homes are on virtio-fs. `~/.cache` and `~/.npm` stay
   guest-local, and the workspace's heavy directories already can be
   (`BNBLOC_SANDBOXD_LOCAL_DIRS`).
3. *Two machines write one home at once.* Space names are per machine kind
   in bnbloc (the own machine's carry their own suffix), so this happens
   only to a client that ensures one name on two machines. The protocol says
   they share it.
4. *A sign-in leaks through the agent.* The guest never holds the value; the
   conformance `secrets` step already checks it is not readable in the
   guest or any answer.

## 4. Build order

1. `durableHomes` and the per-user install places: protocol constant and
   doc, conformance step, the reference implementation, a component
   release (optin).
2. Verify substitution with the real release CLIs in the own machine
   (`NODE_EXTRA_CA_CERTS`), then build sign-ins: the settings list, the
   desktop's own-machine secrets and apply-when-idle, the bridge's
   allowlist for their names, find-a-way's `add-sign-in`.
