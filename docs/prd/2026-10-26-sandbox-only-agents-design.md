# Sandbox-only agents: design (2026-10-26)

Status: **the running half is built** (§10: the ACP core's guest launch, the bridge's
requiresSandbox, checked end to end against the real sandbox); the installing half
(the desktop interface, the manager's target, the store) is not, because two
experiments changed its design (§6, S2 and S4). Revised 2026-10-26 after the
owner's decisions (§0). Builds on the experiment record
[`2026-10-26-sandbox-only-agents.md`](2026-10-26-sandbox-only-agents.md) (what was
tried and measured), `2026-10-09-agent-vm-sandbox.md` (the hybrid mode this sits
beside), `2026-10-21-durable-sandbox-homes.md` and
`2026-10-24-agent-manager.md`. Owners: the agent manager (where an agent is
installed), the ACP core (how it is launched), both components in their own
repositories; the platform adds the two small mechanisms of §4.

A member can choose, per agent, **"install only in the sandbox"**. The agent is
then installed inside their agent sandbox, runs only there, and leaves the host
with nothing: no files, no process, no shared state with anything the member
already runs. The default stays what it is: the agent on the host, confined, its
shell commands in the VM (hybrid), because that mode uses the member's own
installed agent, logins, skills and MCP servers. This is an option, never a
replacement.

## 0. Decided (owner, 2026-10-26)

| # | Question | Decision |
|---|---|---|
| D1 | Which agents? | **Not a fixed list: the member chooses**, per agent, in the manager's page, and may add their own agents by configuration (the manager's `myAgents`). The catalog and the page *tell* what choosing it means for that agent (how it signs in, its size, where it can reach), they do not decide for the member. §2.5, §5 |
| D2 | Whose runs? | **A sandbox-only agent is never borrowed**, and never run for a platform drive. A borrow request for it is refused with the reason. The member's key or login must never be reachable by anyone else's run, so this is a rule, not a default. §4.1, §5 |
| D3 | New plugin interface? | **Yes, but smaller than first drafted.** The desktop does not run commands for the plugin; it prepares the plugin's installer space and hands the plugin a **space-scoped exec token**, which the Sandbox Protocol already has. Checked: a token can run a command in its own space and nothing else (§4.2). |
| D4 | An organization can require it? | **Yes**, a manager setting (`sandboxOnly`): small, and enforced by what the manager offers (§3.6, with its limit stated). |

## 1. Why

- **A second version of an agent shares the first one's state.** opencode 2 keeps
  its data where opencode 1.x keeps its own and changed the shared database's
  schema when it ran there (measured). In the sandbox it has a home of its own.
- **Script installers** (`curl … | bash`) are the agent manager's open decision
  (§6.8 of its record): the script is whatever the host serves at that moment and
  may call `sudo`. Run in a disposable VM under an egress allowlist, with no keys
  and no access to the host, the same script's worst case stays in the VM.
- **Large or untrusted agents** (a Python runtime, a browser, dozens of
  dependencies) need not touch the member's computer.
- The experiment showed the whole path works: install as an unprivileged user,
  survive a machine rebuild, ACP over `vmexec run --stdio`, a conversation that
  survives the idle stop, cancel, and a file written in the VM appearing in the
  host workspace.

## 2. What it is (the end state)

```
 the agent manager's page: per agent  "This computer" | "Sandbox only"
        │  (desktop host API `sandbox`: exec in the manager's installer space)
        ▼
 INSTALL   installer space (egress: the package's declared registry hosts, no keys)
           npm/script as the space user ──▶ the agent store  <workspaces root>/.mounts/<acct>/agent-manager/store/<id>/<ver>/
 REGISTER  agents.d entry: sandbox:"guest", command = the GUEST path of the agent
 RUN       the bridge: the target requires the sandbox ⇒ sandboxed or refused (never the host)
           the core: vmexec run --stdio -- <guest command> ; the store mounted read-only (sandbox-mounts, own runs)
           the workspace mounted at the identical path (as in hybrid)
```

### 2.1 Where an agent lives

An agent's files live in a **store**: a folder on the host under the one machine
share (`<workspaces root>/.mounts/<account>/<plugin>/…`, the place `sandbox-mounts`
already uses), holding **Linux** builds only, written by the installer from inside
the guest. It is not "the host's install": nothing in it runs on the host, nothing
is on the PATH, and it is the manager's own data directory like its npm cache.
Run-time spaces see it **read-only** at the same path in every space of the
member's own machine (a `sandbox-mounts` descriptor, `for: own`). So one 465 MB
install serves every room, a machine rebuild does not lose it (the folder is on
the host's disk), and a room's agent cannot change what the next room runs.

An agent's **state** (its sessions, its config, its caches) is the space's home,
per room, as for any command in that space: a room's conversation stays in that
room.

### 2.1a What the store has to be, after S2 (not built)

§2.1 pictured one folder every room's space mounts read-only at one path. The
sandbox does not allow it (§6, S2): a directory mounted by two spaces is re-owned by
the last one and the other is locked out, and two spaces of a machine cannot share
a guest path. What was shown to work:

- **One canonical store** (the installer space writes it, a host folder), and **one
  APFS clone of it per space** (`cp -c` on the host: 0.3 s, about 1.4 MB of real disk
  for a 410 MB tree), each mounted read-only at a guest path of its own. Three spaces
  ran the same program from their own clones, each unable to write.
- The agent's command line then differs by space (the guest path is per space), so
  the core derives it from the session's execution (`{store}` in the command line
  is the space's mount), and the sandbox-mounts descriptor, which is one list for
  every space of the member, cannot carry it: the bridge has to give each own space
  its clone. That is a change to the bridge's space creation, not only to the
  manager.
- Or, simpler and costlier: **install into each room's own space home** (durable,
  per room), 465 MB per room for the agent in the experiment. No sharing and no
  clone, but a room's first turn waits for the install and the home's 4.7 GB is the
  limit.

**Size, measured (2026-10-26).** The 465 MB is one program, not many files:
opencode 2 ships as a single compiled binary of about 236 MB, and its wrapper
package `@opencode/cli` carries it a second time (`bin/opencode.exe` is a hard link
of the platform package's binary, so the copy costs nothing in one tree but is
counted twice by `du` and twice by anything that copies files one by one). On the
home volume the wrapper install takes 447 MB; installing only the platform package
(`@opencode/cli-<os>-<arch>`, `--ignore-scripts`, run the binary directly) takes
225 MB and runs. The installer should therefore use the platform package when the
catalog names one (`install.package` per platform), which halves the cost of this
agent. It is still 225 MB **per copy**, so per-room installs would multiply it; the
clone store above is what keeps it at one real copy.

Which of the two is the first version is open (§6, S2 follow-up). Until it is built,
a member installs the agent into their sandbox by hand (§10) and the running half
works.

### 2.2 Installing

The manager asks the desktop (§4.2) for its **installer space**: one space per
member on their own machine, namespaced to the plugin, created **by the desktop**
with the store mounted writable, an egress policy of the hosts the signed package
declares for installing (the registries; for a release driver the release host),
**no secrets** and no other mount. The desktop hands the plugin the space's
**exec token** (short-lived, renewed on request) and the sandbox's address. The
manager then runs the typed drivers' commands in that space with the Sandbox
Protocol's own `run`/`exec` (`npm install --prefix <store>/<id>/<version>
<package>@<version>`, or, for an agent whose only channel is a script, §3.4). When
it ends, the installer makes the tree readable by other spaces (`chmod -R a+rX`;
§6, S2), then the manager checks the version the way it does on the host (the
command must report the version asked for) before anything points at it.
Staging, the partial directory, "a failed install leaves nothing behind" and
stall detection (no total time cap) are the host driver's, unchanged.

### 2.3 Registering and running

The manager writes an `agents.d` entry like any other, with
`"sandbox": "guest"` and a **guest** command path. The ACP core, asked to create
a session for that agent:

1. requires an `execution` (the bridge sends one: §4.1); without it the request is
   refused (`bad_request`: this agent runs only in the sandbox), never run on the
   host;
2. starts the agent as `vmexec run --stdio -- <guest command> <args>` with the
   descriptor's environment (the exec token's file, the mount table, as for a
   confined launch), in the session's working directory;
3. otherwise behaves as for any ACP agent: models, turns, interrupt, idle stop and
   `session/resume`, the deadline and the activity steps.

Nothing of the agent's own routing is needed (no command hook, no shell prefix):
the whole agent is in the guest, so every command it runs is.

### 2.4 Updating, resuming, removing

- **Update** installs the new version beside the old one in the store, verifies
  it, then rewrites the registration; the previous version stays for a rollback.
- **A conversation is never lost to an update** (`2026-10-24-agent-manager.md`
  §2.4a): the core's `in-use.json` lists the *guest* paths of live sessions, and
  the manager's cleanup of the store reads it exactly as for host files. The
  core's "the file is gone, continue with the current registration" fallback must
  test the file where it lives: through the mount table, not `stat` on the host
  (§6, S5).
- **Remove** is refused while a room still runs the agent, as for host installs;
  otherwise the store folder for the agent is deleted from the host side (it is
  the manager's own folder).
- **The agent's own self-update** (opencode's `upgrade`, a background service) is
  not used: the store is read-only for run-time spaces, so an agent cannot
  replace itself under a running conversation. A service the agent tries to start
  lives and dies with its guest process.

### 2.5 What the member sees

On an agent's row: *Install on: This computer / Sandbox only*, a size estimate
before installing and the home's free space (the durable home was 4.7 GB in the
experiment), the egress hosts the install will reach, and, after installing, the
agent as a target in rooms named as any other. A sandbox-only agent says so on
its row and where it runs in a room ("runs in your sandbox"). If the sandbox is
off, not ready, or the platform has no backend, the option is not offered and the
row says why (macOS Apple silicon only today).

### 2.6 What an organization can set

Manager settings, org scope, beside `allowed` and `pinned`:
`sandboxOnly` (agent ids offered only as sandbox-only) and, per agent already in
the catalog, nothing else: which hosts an install may reach is the signed
package's declaration (§4.2), never a setting, because a setting that widens
egress for every member's installer would be a new way to reach the internet from
every computer.

## 3. The parts

### 3.1 The agent manager (component)

- New **install target** `sandbox` beside `host` for the `npm` and `script`
  drivers; the choice is per agent, in its state (`placement`).
- A **store** per agent version under the share, an in-use rule over guest paths,
  size reporting.
- Catalog fields: `guest`: `{platforms: ["linux-arm64","linux-x64"], needs:
  ["node>=22"]}` and, for agents that authenticate by key, `credentials`:
  `[{env: "OPENAI_API_KEY", hosts: ["api.openai.com"]}]` (§5).
- The page: the choice, sizes, hosts, reasons.

### 3.2 The ACP core (component)

- `sandbox: "guest"` in an agent entry (validated: an absolute command, no host
  lookup, `args` as given), the launch of §2.3, refusal without an `execution`.
- `in-use.json` lists guest paths; the relaunch fallback tests through the mount
  table.
- `check` reports a guest agent as "runs in the sandbox" and probes it **in the
  guest** when a sandbox is available, otherwise as "needs the sandbox", never by
  starting it on the host.

### 3.3 Workspaces and permissions

The hybrid path already creates a sandboxed session's workspace in the sandbox's
working directory, under the shared root and mounted at the identical guest path
(`SandboxExecution.Workdir`/`Mounts`). A guest-only launch uses exactly that, so
the host paths in ACP messages mean the same in the guest. The experiment's
`EACCES` (the core's 0700 directories against another identity in the guest) is
answered the way hybrid mode answers it for the commands it routes: the workspace
the bridge hands the core is the sandbox's own directory, created readable by the
space user. S1 and S2 check that the same holds for what a guest agent itself
creates and for the store.

### 3.4 Script installers

A catalog entry may carry `install: {method: "script", url, host}` **only for the
sandbox target**. The script is fetched and run in the installer space under its
egress allowlist (the `host` and what it names in the package's declaration), as
the unprivileged space user: no `sudo`, no keys, no host access. The result must
pass the same check (the command reports the version). The host target never
gets a `script` method (unchanged). This is what settles the manager's §6.8: the
one-click installer for an agent whose only channel is a script exists, and only
where its worst case is a VM.

### 3.5 Egress

Two policies, both narrower than `public`: the installer space's (the hosts the
signed package declares for installing) and the run space's (the hosts of the
agent's model API, from `credentials[].hosts` and the agent's own declaration,
plus nothing else by default). A member may widen the run-time list for their own
runs the way they allow hosts in the sandbox today; the manager shows what an
agent is allowed to reach. **Checked (S4), root cause found and fixed:** an `allowlist` machine refused every
unlisted host but also failed the listed registry's TLS handshake (curl: unexpected
EOF; npm: network error). The cause is in the sandbox service, not the network: a
rule that names a host can only be enforced by reading the TLS hostname, which
needs the TLS interception, and the service switched that on only when the
machine had secrets. Control: the same allowlist machine with one placeholder
secret reached the registry (HTTP 200, issuer "microsandbox CA"), `npm install`
through a registry-only allowlist worked, and an unlisted host was refused. (Not
the cause, disproved by a control: the size or fragmentation of the ClientHello.) **Fixed in
`bnbloc-sandboxd` 0.1.9** (intercept whenever an allowlist has a name rule; an
allowlist of addresses only stays plain), checked live on the new binary: listed
HTTPS 200, `npm view` works, two unlisted hosts refused. The conformance suite's
`egressPolicy` now asserts both over HTTPS (it fails on 0.1.8). So the installer
space can run under an allowlist of the registry hosts and the §3.4 argument for
the script method holds; the guest then trusts the interception CA, which it
already does for any machine with secrets. Not yet published (the desktop pins
the sandbox service version).

### 3.6 Policy: `sandboxOnly`

A manager setting, organization scope, beside `allowed` and `pinned`: agent ids
a member may install **only** into the sandbox (a lines setting, validated like
`allowed`). The page then offers them with one choice. It is small: one setting,
one filter where the page decides what to offer, one test.

Its limit, stated rather than hidden: it governs **what the manager offers**, as
`allowed` and `pinned` do. A member who installs the same agent some other way,
or writes their own `agents.json` line, is outside it. An organization that needs
a boundary rather than a policy has the sandbox mode `all` (every own run in the
sandbox), which is the sandbox component's setting, not this one. Which hosts an
install may reach is the signed package's declaration, never a setting: a setting
that widens egress for every member's installer would be a new way to reach the
internet from every computer.

## 4. What the platform adds (and why that is all)

### 4.1 A target that requires the sandbox, and is never borrowed

`Target.requiresSandbox` (Core Protocol v1, optional, reported by the core). The
bridge's `decideSandbox` treats such a target like content that is **forced**:
sandboxed when the sandbox is ready; otherwise refused with a reason, **never
run on the host**, whatever the member's sandbox mode (`off`, `borrowed`, `all`).
That machinery exists for foreign content (`forced`, `sandboxUnavailable`); this
adds a second reason to be forced and one field on the wire.

The same flag means **own runs only** (D2): a borrowed drive or a platform drive
of such a target is refused with "this agent runs only in its owner's sandbox and
cannot be borrowed". Why a rule and not a case-by-case judgment: the agent's model
key (a protocol secret on the own machine) or its login (a file in the own
space's home) exists only for the owner's runs; a run for someone else on the
shared machine has neither, so it could not work, and giving it either would hand
over the owner's credential. One flag, two effects, a conformance case for each:
the flag with the sandbox off is refused; the flag with a borrow is refused.

### 4.2 A plugin host API `sandbox`: a scoped token, not a proxy

`permissions.host: ["sandbox"]` on a plugin package (the table of
`pluginHostPermissions`, beside `chat`). A first draft had the desktop run the
plugin's commands for it (a proxy of `exec`). That would have made the desktop
re-implement streaming, signals and limits that the Sandbox Protocol already has,
and made it a place where commands are decided. The smaller interface:

- `POST {BNBLOC_PLUGIN_HOST_URL}/v1/sandbox/space` (the plugin's host token, as
  `chat` works) → `{url, machine, space, token, expiresAtMs}`.
- The **desktop** ensures the plugin's installer space on the member's **own**
  machine (the name derived from the plugin's name, never chosen by the plugin),
  with the mounts and egress the plugin's **signed package** declares
  (`sandbox.store`, `sandbox.installHosts`) and **no secrets**, and mints an exec
  token for that space only (`execTokens`, existing). Asked again before it
  expires, it mints a new one.
- The plugin then speaks the Sandbox Protocol's `run`/`exec` to that address with
  that token. Nothing else of the sandbox is reachable.

**What the token can do was checked** (2026-10-26, the real sandbox service, a
space `installer` and another space `other` on one machine). With the installer's
token: a command in its own space runs (as the space's unprivileged user); every
other call is `403 forbidden` ("an exec token may only run commands in its own
space"): a command in another space, a machine-level command as root, listing or
creating machines, creating or altering a space (so no mount can be added),
deleting its space, minting another token, files as root or in the space, and
`/discover`. So a plugin holding it can run commands in one confined space and do
nothing to the sandbox itself.

A plugin without the permission gets nothing: the sandbox's admin bearer, other
spaces and other machines are never reachable. Reason: identity and confinement
(the plugin is untrusted code; the desktop is the one holder of the credential and
the one that decides a space's mounts and egress). The interface names no agent,
no installer and no registry. It is audited as an action of the plugin for the
member (the desktop records each token it issues).

Everything else (the store mount, the registration, the launch, the page, the
catalog, egress values) is the manager's and the core's data and code.

## 5. Sign-in: what each choice means, shown to the member (D1, D2)

The 2026-10-21 record decided **not** to build sign-ins through the sandbox's
`secrets`: a tool that checks its token's format locally, exchanges it for
another token, or signs requests is not served. A guest-only agent forces the
question, so the design is: **the member decides, with the consequence on the row
before they choose**, and the catalog says which of three cases an agent is
(a configured agent says it in its line):

| The agent signs in with | Where the credential lives | What the page says |
|---|---|---|
| **Nothing** (a free or local model) | nowhere | nothing to set up |
| **An API key sent as a header to named hosts** (`credentials: [{env, hosts}]`) | the member's store on their computer; the own machine gets it as a protocol **secret**: the guest's `$NAME` is a placeholder, the value is substituted on egress to those hosts only; the agent never reads it | "Your key stays on your computer; the sandbox sends it only to <hosts>" |
| **A login that writes a token file** (a browser or device flow) | the guest's durable home for the space, **readable by the agent** (like the agent's own login on the host for an own run) | "Its sign-in is stored in your sandbox, where the agent can read it. It is never available to anyone else's run." |

Two rules make the third case acceptable rather than the exact failure the earlier
record warned of:

1. **It is never borrowed (D2).** The credential exists only for the owner's own
   runs and is a file in the owner's own space's home; no other run is given that
   space or its home. An agent already reads its own login on the host for an own
   run: the sandbox is not worse, and for the key case it is better.
2. **A login must be completable.** A browser callback to `localhost` cannot
   complete inside the guest, and the guest has no terminal for the member
   today. An agent is offered the third way only when its catalog entry (or the
   member's configured line) names a login that runs without a terminal: a
   device-code flow the manager runs as an operation and shows (the URL and the
   code in its log, the member opens the URL), `loginArgs`. An agent without one
   shows the choice disabled with that reason; it is not hidden, and the member
   can still run it on this computer.

What substitution does not cover is the earlier record's list (local format
checks, a token exchange in a body, encoded or signed credentials): an agent whose
key is of that kind is marked so in the catalog and the first way is not offered
for it.

## 6. What must be verified before building

Each is an experiment like the first, on the real sandbox, not a reading.

| # | Question | Why it can sink the design |
|---|---|---|
| S1 | A guest agent writes in its workspace, and the core and host read it; with the workspace created by the bridge's path | the `EACCES` of the experiment |
| S2 | **Done 2026-10-26, and it changed the design (§2.1a).** Executing from a read-only mount works (script and Node program, writes and deletes refused). **One host directory cannot be mounted by several spaces:** the mount root is re-owned 0700 by whichever space ensured it last, the first is locked out and a third cannot read it, and a guest path may not be used by two spaces of a machine. | the shared store of §2.1 cannot exist as drawn |
| S0 | **Done 2026-10-26:** the exec token's reach (§4.2): own space only | D3 |
| S1 | **Done 2026-10-26:** a guest agent reads a file the core wrote into the workspace (mode 0600) and writes one the host sees; the workdir mount maps ownership, so no mode change is needed | the `EACCES` of the first experiment came from a directory the core made outside the mount, not from the workspace |
| S3 | A key agent through `secrets`: the placeholder works against a real model host, the value is never visible in the guest (`env`, `/proc`, the agent's config) | sign-in §5 |
| S4 | **Done 2026-10-26, half answered (§3.5):** an `allowlist` machine refuses every unlisted host (github, example.com, a raw IP), but also **fails the listed one**: `registry.npmjs.org` answered a TLS error for curl and npm could not install. How a listed HTTPS host is let through is not known. | the installer's allowlist is the safety of the script method |
| S5 | The core's "program gone" test and `in-use.json` with guest paths through the mount table | a conversation must not be lost (the agent manager's §2.4a) |
| S6 | Hermes's installer in the installer space completes within the home's capacity (it did not finish in 25 minutes in the experiment: a large git checkout over the VM network) | §3.4's claim |
| S8 | A device-code login runs as a manager operation in the guest and its URL and code reach the page; the token file it writes is readable only in that space | §5, the third way |
| S7 | The sandbox's two machines (own, shared): the store visible on both as a read-only mount for `own`, and absent for borrowed | D2 |

## 7. Design review

| Lens | Answer |
|---|---|
| Actors | The member (own machine, own spaces); a borrower (cannot run a sandbox-only agent: refused, no keys); an organization admin (`sandboxOnly`); another space on the same machine (reads the store read-only, never writes it, never sees another room's home); the plugin (a token for its installer space only: it cannot reach another space, a machine or the sandbox itself, checked); a platform drive (refused, like a borrow). |
| Time and lifecycle | A conversation survives an idle stop and an update (the in-use rule over guest paths, the fallback). The store survives a machine rebuild (host disk), the sandbox restarting and the desktop updating; the homes survive as durable homes already do. A desktop restart ends the core's sessions as it does for every agent. An install in progress when the sandbox stops fails and leaves a partial directory that the next start sweeps. |
| Scale and cost | 465 MB for one agent, the home 4.7 GB: the page shows sizes before install and refuses what cannot fit. One store serves every room (no per-room copy). Versions are kept as for host installs (current, rollback, held by a session). |
| Failure and recovery | Sandbox off, not ready, no backend: the option is hidden or the agent refused with the reason, never run on the host. An install that fails or reports another version registers nothing. A key the agent's API rejects is the agent's error, in the reply. A script that stalls ends by stall, not a total time. |
| Boundaries and privacy | Nothing of the agent on the host; the store holds Linux files the host cannot run. An API key never enters the guest (a placeholder, substituted on egress to its hosts); a login file does, in the owner's own space's home, readable by that agent only and never by another run (D2). The plugin holds a token for one space and never the sandbox's bearer; the installer has no keys and an allowlist. The workspace is the one shared place, as in hybrid. |
| Abuse and untrusted input | The installer runs untrusted package scripts: unprivileged, no sudo, no keys, allowlisted egress, in a VM, with a store the run spaces mount read-only. A prompt-injected agent in a run space cannot modify the store (the experiment showed a confined process cannot write the host store; this adds a read-only mount). A hostile script can fill the home: bounded by its size, said on the page. |
| One owner | The Sandbox Protocol owns spaces, mounts, egress, secrets; the desktop owns the host API and the machine; the manager owns installs and the store; the core owns launches and `in-use.json`; the bridge owns the "forced" decision (one added reason, not a second path). |
| Defaults and fallbacks | The default is unchanged (host, hybrid). Nothing is sandbox-only until the member chooses it or the org lists it. No silent fallback to the host, ever. An agent not offered says why. |
| External contracts | The Sandbox Protocol's `exec`, `shares`, `readOnlyMounts`, `secrets`, `egressPolicy`, `durableHomes`, `execTokens`, all in the discovery document: the feature is offered only where they are advertised. Each verified in §6 before it is relied on. Each agent's Linux build and ACP launch line probed. |
| Load on users and agents | One choice per agent and a few facts on the row. Agents see nothing new (no tool, no prompt text). |
| Release and compatibility | The core first (a new `sandbox: guest` value an older core rejects visibly), then the bridge and desktop (the target flag, the host permission), then the manager; each additive, optin. A core or bridge without the flag simply never forces. The existing hybrid runs are unaffected; verified with the existing long-task scenario before and after. |
| Observability | The page and the core's log say where an agent runs and why a run was refused; the install log is the manager's ring; `bnbloc-acp check` shows `runs in the sandbox`; the desktop audits each host `sandbox` call for the plugin. An end-to-end check: install in the sandbox, run a turn, update, run a turn, with the host's file listing unchanged. |
| AI capability | The decision "this agent would collide with what you already run, or is untrusted: install it only in the sandbox?" is a proposal with a reason, shown on the row (the manager's existing find-a-way style card): AI proposes, the member decides. A failed install is explained by the planned `diagnose-agent-install` assist. No model is in install, update or launch. |

**Pre-mortem.** It shipped and a month later it failed badly:

0. **A borrower or a platform drive reached the owner's credential.** The target flag refuses both, a conformance case each, and the credential lives only on the own machine and in the own space's home.
1. **Members lost a room's agent because the store changed under it.** The in-use
   rule is the same one that fixed the host case, now over guest paths; S5 tests
   it with a real conversation across two updates, the store is read-only to run
   spaces, and the fallback never touches a launch whose files exist.
2. **A sandbox-only agent ran on the host after all** (a mode, a race, an older
   bridge). The target flag makes the bridge refuse rather than fall back, the
   core refuses a launch without an `execution`, and a conformance case for each.
3. **A member chose it and could not sign in.** The choice shows how the agent signs
   in and what that means before it is made; an agent whose login cannot complete
   without a terminal has the choice disabled with the reason, never hidden and never
   a dead end (it still runs on this computer).
4. **The installer was a way out** (a script reaching an arbitrary host, or
   reading a key), **or the plugin's token was more than it seemed.** No keys, an
   allowlist declared in the signed package, no sudo, verified by S4 before any
   script method exists; the token's reach was verified (S0): own space, run only.
5. **The disk filled** (a 4.7 GB home, a Hermes checkout). Sizes before install;
   an agent that does not fit is refused; cleanup of versions no session holds.

## 8. Slices (each a slice of §2, none a stopgap)

1. **The core**: `sandbox: guest`, the launch, refusal without an `execution`,
   `in-use.json` and the fallback over guest paths, `check`; tested against a fake
   sandbox and then the real one (S1, S5).
2. **The bridge and the Core Protocol field**: `Target.requiresSandbox`, `forced`
   treatment, the conformance cases.
3. **The desktop host API** `sandbox` (the installer space ensured by the desktop,
   the scoped token, renewal, the audit), the package declaration
   (`sandbox.store`, `sandbox.installHosts`), the store folder and its
   `sandbox-mounts` descriptor (S2, S4).
4. **The manager**: the target `sandbox`, the store, the page (the choice with its
   consequences, sizes, hosts), the in-use rule, `sandboxOnly` (a day), the key
   setting and the secret on the own machine (S3), device-code login (S8).
5. **Script installers** in the installer space (S6), then Hermes.

## 9. Not in this record

- Linux and Windows hosts (no local sandbox backend yet), and a **server-side**
  sandbox for members whose machines cannot run one: the same seam, a different
  backend, a later phase of the sandbox record.
- A terminal into the guest to sign in interactively (agents whose login needs
  one stay host-only until there is one).
- Borrowed runs of a sandbox-only agent: never (D2), not deferred.
- Sharing one store between members.
- Replacing hybrid mode: it remains the default and the way for agents that use
  the member's own installation, skills and login.

## 10. As built (2026-10-26): the running half

**Core Protocol and the bridge** (bnbloc `74558287`): `Target.requiresSandbox`; the
bridge treats a run of such a target as one the sandbox is mandatory for (sandboxed,
refused, or waited for while the sandbox starts, never the host, in every mode), and
refuses a borrow or platform seat of it ("cannot be borrowed or used by anyone
else"). The owner's own run keeps the owner's forwarded environment. Tests: the
decision table, and through the whole bridge (a borrow and an own run refused with
the mode off, the owner's run sandboxed, an unmarked agent unchanged); each fails
with the logic switched off.

**The ACP core** (bnbloc-acp `a3902ab`): `"sandbox": "guest"` in an agent entry; the
command is a command line of the guest, started as `vmexec run --stdio -- <line>`
(arguments quoted for the guest shell); the agent is listed at once, unprobed on the
host, with `requiresSandbox`; a request without a sandbox is refused; no `env`,
`envProvider` or `models` yet; the host's "program is gone" fallback is skipped for
it. Seven tests, three of them failing with their logic switched off.

**Checked against the real sandbox, opencode 2 and a real model** (the sandbox
service, the core and the helper as shipped; the descriptor built by hand because
the desktop's bridge is not involved): the target is listed with `requiresSandbox`; a
request without a sandbox is refused (400); a guest turn answers; the agent reads
`.room/INDEX.md` the core wrote (mode 0600) and answers from it; a file the agent
writes appears in the host workspace; after the idle stop the same conversation
continues in a new VM process and recalls the content; the only process on the host
is the `bnbloc-vmexec` helper.

**Using it now, by hand** (until the installing half exists): install the agent in
the sandbox's space home as its user (e.g. `npm install --prefix ~/agents/x …`), then
a line in `agents.json` with `"sandbox": "guest"` and the guest command. The agent
runs per room in that room's space home, so it is installed once per room (the space
name is per room and member).

**Not built:** the desktop's `sandbox` host interface and its installer space; the
manager's install target and page; the store and its clones (§2.1a); `sandboxOnly`;
the key as a protocol secret (§5); the device-code login; the egress allowlists.
**Not run:** the desktop app itself (the bridge change is tested, not run in the
real app), and a guest agent that needs a login or a key.
