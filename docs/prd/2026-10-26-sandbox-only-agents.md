# Agents installed only in the sandbox (2026-10-26)

Status: experiment done, nothing built. Records what was tried, what it showed
and what is undecided. Owner (when built): the agent manager (where an agent is
installed) and the ACP core (how it is launched), both components; the platform
adds nothing it does not already have (§4).

A member may choose to keep an agent off their computer entirely: it is
installed inside their agent sandbox (the microVM), runs there, and leaves the
host with nothing to clean up or to collide with. This is **an option beside**
the hybrid mode of `docs/prd/2026-10-09-agent-vm-sandbox.md` (the agent on the
host, confined, its shell commands in the VM), not a replacement: hybrid stays
the default, because it uses the member's own installed agent, logins, skills
and MCP servers.

## 1. Why

- **opencode 2** keeps its state in the same directories as opencode 1.x
  (`~/.config|.local/share|.local/state|.cache/opencode`) and changed the shared
  database's schema when it ran in a directory 1.x had used (measured in an empty
  home: the table set differs after 2.x ran). A member's real `opencode.db` was
  14.7 GB. It also runs a user-level background service the ACP core does not
  own. On the host this is a risk to the agent they already use; in the sandbox
  it has a home of its own.
- **Script installers** (`curl … | bash`): the agent manager never ran one
  (`docs/prd/2026-10-24-agent-manager.md` §6.8) because the script is whatever the
  host serves at that moment and may call `sudo`. In a disposable VM with a
  limited egress policy the same script's worst case stays in the VM.
- **Large or untrusted agents** (a Python runtime, a browser, a dozen
  dependencies) need not touch the host at all.

## 2. What was tried (2026-10-26, macOS Apple silicon, the real sandbox service)

A second `bnbloc-sandboxd` (own port, token, data directory; the desktop's was
never touched, but see §5), the real ACP core, the real `bnbloc-vmexec`, a free
model.

| Step | Result |
|---|---|
| Guest toolchain | Linux aarch64; Node 24, npm 11, Python 3, curl, git; no sudo for the space user; a machine starts in 1.4 s |
| Install opencode 2 as the space user into its durable home | works, 60 s, 465 MB; the host has no file named for it and no process |
| Delete the machine, create it again | the install is still there (durable home) |
| ACP over `vmexec run --stdio -- <agent> acp` | `initialize` and `session/new` work; the same opencode 2 whose `session/new` failed on the member's host environment worked here |
| Through the real ACP core | `available`, full model list; a turn replies; a file the agent writes in its workspace appears on the host (the workspace is a shared mount); a conversation survives the idle stop and a new VM process (`resumed=true`) and recalls its content |
| Latency | first turn (VM start, session, model) 6.7 s; a warm turn 2.0 s |
| Cancel | `interrupt` while the turn was verified running ended it in 2 s; the session answered the next message |
| Hermes's `curl \| bash` in the VM (`--skip-browser --skip-computer-use`) | **not finished**: its `git checkout` of a large repository over the VM's network was still running after more than 25 minutes (about 400 MB fetched); not a verdict on the idea, a measurement of one slow step |

## 3. What the experiment found that has to be designed

1. **ACP messages carry host paths** (the session's working directory, file
   locations). The agent in the VM cannot see them unless the workspaces root is
   mounted at the **same path** in the guest (the experiment mounted the ACP
   home at an identical guest path). The hybrid mode avoids this by creating the
   workspace in the sandbox's own working directory.
2. **The ACP core makes its directories 0700**, and the guest user is another
   identity: the agent failed with `EACCES` on its own working directory. The
   experiment relaxed the modes with a background `chmod`; a real design needs
   guest-readable workspaces for this mode only (not weaker modes for host runs).
3. **Home capacity.** The durable home is its own volume (4.7 GB in the
   experiment, 769 MB used after one agent); the root disk is 3.9 GB. A 465 MB
   agent and a growing Hermes checkout share that home. An agent that does not
   fit must say so before installing.
4. **A shared mount needs the exec token's mount table** (`BNBLOC_VMEXEC_MOUNTS`)
   or `vmexec` refuses: the hybrid bridge supplies it; a guest-only launch must
   too.
5. **Sign-in.** Not exercised (a free model needs none). The agent's own login
   would live in the guest's durable home, which `durable-sandbox-homes` §2.2
   decided not to automate through chat. Options: the member signs in once in
   the guest (a terminal into the space), or the model credential goes through
   `secrets` / `hostProxy` and never enters the VM. Undecided.
6. **Egress.** The experiment used `public`. A guest-only agent should run under
   an allowlist naming its model hosts; not tried.
7. **Availability.** The local backend is macOS Apple silicon only; elsewhere the
   option is not offered, and the agent manager must say so.

## 4. Placement

| Part | Content / component / platform | Why | What an organization would swap |
|---|---|---|---|
| Installing an agent into the guest (the `npm`/script driver run as the space user through the Sandbox Protocol `exec`) | Component: the agent manager, a new install target beside "this computer" | It is an install method; the protocol already has `exec`, durable homes and spaces | Its own manager build |
| Launching the agent in the guest (`vmexec run --stdio`, the mount table, workspaces at an identical path) | Component: the ACP core, a new launch mode beside host and hybrid | It is how the core starts an agent | Another core |
| Which agents may be guest-only; egress hosts; capacity | Component settings (org and member), the catalog's own fields | Policy values | The values |
| The sandbox, its protocol, exec tokens, shares, durable homes, egress, secrets | Platform: existing | Confinement, isolation, audit | n/a |
| The member's choice "install only in the sandbox" | The manager's page, a per-agent choice | A page of a component | A different page |

Platform tests: *Name* (no agent or installer named in the platform), *Fork*
(nothing to patch), *Replace* (a second manager or core can offer the same),
*Reason* (nothing new is platform).

## 5. What went wrong in the experiment

Cleaning up, `pkill -f bnbloc-sandboxd` stopped **every** sandbox service of the
user, including the member's own desktop's. The desktop's supervisor started it
again within about a minute and recreated two machines; whatever sandboxed run
was in flight then was cut. The lesson is promoted to AGENTS.md ("Stop and delete
only what you started"). The experiment's own service ran on its own port and
data directory and was gone before any of this.

## 6. Open items

1. Guest-readable workspaces for this mode (§3.2) and the mount table (§3.4).
2. Sign-in (§3.5) and egress (§3.6): decide, then try with an agent that needs a
   login and a real allowlist.
3. Hermes's installer in the guest, with a faster fetch or a shallow clone, to
   learn whether it completes within the home's capacity.
4. The agent manager's in-use rule (`docs/prd/2026-10-24-agent-manager.md` §2.4a)
   for files in the guest: the core would publish guest paths too.
5. Whether the page should offer "install only in the sandbox" only where the
   sandbox component is installed and its machine runs.
