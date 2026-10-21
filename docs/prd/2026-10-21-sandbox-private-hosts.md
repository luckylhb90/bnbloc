# Private hosts for a member's own sandboxed agents, asked for by the AI (2026-10-21)

Status: built (2026-10-21). Slice 1: `egressHosts` in the protocol, the
conformance step `egressHosts` (`SANDBOX_PRIVATE_HOST`), the reference
implementation; the full suite passes against it with an internal host
listed. Slice 2: `bnbloc-own` and its apply rule (`plan_own_machine`), the
bridge's machine per drive (`BNBLOC_SANDBOX_OWN_MACHINE`,
`driveKey.ownMachine`). Slice 3: the sandbox's configuration as its own
component settings, set in Components (§2.3), and find-a-way's
`allow-host` (§2.4). Owner: the Sandbox Protocol (`sandbox/protocol`,
`docs/architecture/sandbox-protocol.md`), the sandbox settings contract
(`core/pkgmeta/sandbox_settings.go`), the desktop's sandbox adapter
(`apps/desktop/src-tauri/src/internal_ext/sandbox*.rs`), the bridge's
sandbox runtime (`services/bridge/sandbox.go`), find-a-way
(`docs/prd/2026-10-20-never-a-dead-end.md`).

A member whose own agent runs in the VM sandbox can let it reach a named
private host (the team's package registry, its CI) instead of the sandbox
blocking every private address. The organization opens its hosts for
everyone and decides whether members may add their own; the AI notices a
blocked host in a reply and proposes it; the member allows it with one
click. All of it is the sandbox component's own settings, set where every
component is set up: Components.

## 1. Why

- QA 2026-10-21: in a release room, maya's own Claude Code agent (desktop
  sandbox mode `all`) installed a public tool but could not install the
  team's release tools: the sandbox could not resolve
  `npm.internal.example` and `ci.internal.example`. The agent stopped and
  told her to fix DNS or use a machine with intranet access. On her own
  computer both hosts work.
- The owner asked for settings "so the AI asks the member whether to turn
  something on, instead of everything being disabled".
- The open item of `2026-10-09-agent-vm-sandbox.md` ("tenant-admin-managed
  egress rules (allowlisted private hosts) as an explicit policy input")
  is this.

What blocks it today, checked against the code:
- The desktop creates the one machine `bnbloc` with egress `public`
  (`create_machine_request`): the public internet, never a private, LAN,
  loopback or link-local address.
- The reference implementation enforces egress per machine on the host and
  does not offer `spaceEgress`; a per-room rule cannot be enforced there.
- The machine is shared by the member's own drives **and** by borrowed and
  platform drives (each in its own space). Opening a private host on it
  would open it to a teammate's borrowed prompt.
- `public` takes no rules (protocol and implementation both refuse them),
  and `allowlist` drops the public internet.
- Its DNS refuses an answer with a private address (rebind protection)
  unless an address rule covers that address; a name rule alone is not
  enough. That is the "cannot resolve" the agent saw.
- Its egress policy is fixed when the machine is created: `start` after
  `stop` boots the stored VM with the same policy (`Vm::start`), so a
  change needs a recreate (create measured 0.4–0.6 s; the bridge
  re-ensures its space every turn anyway).

## 2. What it is (the end state)

### 2.1 Two machines: own and shared

- The desktop runs two machines when the member has private hosts: `bnbloc`
  (as today: borrowed, platform and, without private hosts, own drives)
  and `bnbloc-own` (the member's own drives only, egress `public` plus
  their private hosts). With no private hosts there is one machine, as
  today.
- The bridge picks the machine per drive: own drive → `bnbloc-own` while
  the desktop hands it over, else `bnbloc`; borrowed and platform drives →
  `bnbloc`, always. A borrower's prompt never runs where a private host is
  open. Both machines share the same workspaces root; a session never
  moves between machines (the session key carries the machine, as it
  carries the execution kind today).
- The desktop hands `bnbloc-own` over (`BNBLOC_SANDBOX_OWN_MACHINE`) once
  it runs with the member's hosts, and withdraws it when it is no longer
  wanted (mode not `all`, no hosts) or could not be created; the handoff
  changing restarts the bridge through the stabilized path, which waits
  while turns run. While handed over, the machine briefly missing (a
  recreate) is `sandbox_starting`, retried; an own drive never falls back
  to `bnbloc` mid-session.
- The own machine follows one rule (`plan_own_machine`): created beside a
  ready shared machine (whose prewarm pulled the image), recreated when its
  hosts differ, deleted when not wanted, and never recreated or deleted
  while an own turn runs on it: the bridge counts those in `turns.json`
  (`ownMachine`), and the watch (every 15 s) applies a waiting change.

### 2.2 The protocol: `public` with extra hosts

- New capability `egressHosts`: mode `public` may carry `rules`, hosts
  reachable **in addition** to the public internet, private addresses
  included. Same rule shape as `allowlist` (an exact DNS name, a
  `*.suffix` of at least two labels, an IP literal or CIDR; port 0 = any).
- The implementation must admit a private DNS answer for a listed name
  (and only for it) and allow connections to it; every other private
  answer stays refused. Enforcement stays outside the guest.
- Conformance step `egressHosts`: a listed private host resolves and
  connects; an unlisted private host neither resolves nor connects; the
  public internet still works; a listed name does not open the address of
  an unlisted one.
- The reference implementation opens a listed name **by its addresses**:
  it resolves the name on the host when the policy is applied (create, a
  restore that sets egress) and adds a port-less address rule for each
  private address. Checked live against microsandbox 0.7.7 (2026-10-21):
  - its DNS rebind check admits a private answer only for such an address
    rule (`policies_allow_rebind_address` reads `Cidr` and `Group`, never
    `Domain`), so a name rule alone does not resolve;
  - a name rule makes it hold each connection for the TLS hostname, and
    TLS to the private host then failed (`unexpected eof`); address rules
    alone connect (`/-/ping` answered 200);
  - so another name on the same addresses is reachable too (two internal
    names behind one load balancer both opened), and the port is not
    held; any other private address stays closed, the public internet
    open, metadata closed. Wildcards are refused (nothing to resolve
    ahead). The protocol allows a stricter implementation.
  A listed IP literal keeps its port.

### 2.3 The sandbox's settings are its own, set in Components

Revised 2026-10-21 (the owner: "a component's configuration is kept by
the component itself and set in Components"). Slice 2 first put the mode,
the forwarded environment and the hosts in desktop Settings → Agent
sandbox; that section is gone. A sandbox package declares them in its own
manifest `settings` (`docs/prd/2026-10-19-component-settings.md`), and
they are set in the one settings dialog on its Components card, like any
component's:

| Key | Scope | Type | Default | What it does |
|---|---|---|---|---|
| `mode` | member | choice `off` \| `borrowed` \| `all` | `borrowed` | Which of the member's runs execute in the VM (`BNBLOC_SANDBOX_MODE`). |
| `forwardEnv` | member | lines | — | Names or `PREFIX*` patterns let into the member's own sandboxed runs (`BNBLOC_SANDBOX_ENV`; `pkgmeta.ValidForwardEnv`). |
| `privateHosts` | member | lines | — | Hosts the member's own sandboxed runs reach (`ValidPrivateHost`); the card's Allow adds one. |
| `orgHosts` | org | lines | — | Hosts the organization opens to every member's own sandboxed runs, without asking. |
| `memberHosts` | org | choice `ask` \| `off` | `ask` | Whether a member may allow a host of their own. |

- **The contract.** The keys are the sandbox kind's contract
  (`wire.SandboxSetting*`, `pkgmeta/sandbox_settings.go`): a package that
  declares one declares it in that shape (`validateSandboxSettings`), and
  may declare more or fewer; an undeclared one takes the platform's
  default. The value rules beyond a type (each `forwardEnv` line a
  forwardable name, each host line a private host) are
  `pkgmeta.CheckSandboxSettingValue`: the gateway holds an organization's
  save to it (`CheckKindSettings`), the desktop a member's (its copy,
  built from the two pinned rules).
- **Where values live.** As for every component: the organization's
  sealed on the gateway, the member's in their member store on the desktop
  (`component-settings/sandbox@<name>.json`). The sandbox reaches members'
  computers, so the desktop reads the organization's values
  (`GET /v1/components/{name}/settings/resolved?kind=sandbox`) each time
  the catalog changes (a settings save pushes `components_changed`).
- **What the desktop does with them** (`sandbox_settings`): the mode and
  the names from the member's values; the own machine's hosts are
  `orgHosts`, plus `privateHosts` when `memberHosts` is not `off`. Until
  the organization's values are read, none of the member's hosts opens
  (fail closed). A change reconciles the machines (the own one when no own
  turn runs there) and, through the bridge's stabilized restart, the bridge
  env; it never restarts the sidecar.
- **The dialog** shows the form (each scope the viewer may set) and, on
  this computer, the sandbox's state: the VM, the hosts open now ("not open
  yet" while a change waits), Restart VM, Apply again, Reload shell
  environment.
- A host opens on any port: the reference cannot hold a name's port
  (§2.2), and the settings' help says so. Used only in mode `all` (in
  `borrowed` the member's own runs are on the host, which reaches the hosts
  itself).

### 2.4 The AI proposes the host: find-a-way's `allow-host`

- **The fact.** The bridge marks where a turn's commands ran
  (`AgentJob.sandboxed`: `own` for the owner's own sandboxed run, `shared`
  for a borrowed one or one wearing someone else's content). The room
  holds the member's jobs, so the card knows the reply's run was their own
  agent in their sandbox; the desktop says whether its settings let them
  allow a host and its runtime can open one.
- **The assist.** find-a-way gets both in `<capabilities>` ("the reply's
  run was the member's own agent in their agent sandbox, which reaches the
  public internet but no private host they did not allow"; "the member may
  allow a private host: yes|no") and has the action `allow-host`: when the
  reply shows the agent could not reach a host on the member's network,
  `target` is that host. Its instructions are content
  (`bnbloc-default-content/packages/find-a-way`).
- **The card** holds the target to the host rule (the web's copy of
  `ValidPrivateHost`, pinned by the same fixture), drops it unless the run
  was the member's own sandboxed one and they may allow a host, and never
  offers a host already open. The click asks first, in plain words ("let
  your own agents' sandboxed runs reach `npm.internal.example` on any port,
  and anything else at its address; runs by other members never reach
  it"); Allow adds the host to the member's `privateHosts`; the card then
  waits until the own VM and the bridge have it and offers **Ask again**
  (the same request to the same agent). With no assist model the fallback
  offers `allow-host` with a host field.
- Nothing in the agent changes.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member decides for their own agents; admins bound it; a borrower and a platform drive never reach a private host (§2.1). An agent never adds a host: the member's click does. |
| Time and lifecycle | A change applies when no own turn runs (never-stop-live-runs); a recreate takes under a second; a host the policy drops is removed at the next apply. |
| Scale and cost | A second VM only for members with private hosts; idle cost is memory (paused when idle is a follow-up). |
| Failure and recovery | `bnbloc-own` not ready → own drives run on `bnbloc` as today (the host just stays blocked); the card says the change did not apply and why. Organization values not read yet → none of the member's hosts opens. |
| Boundaries and privacy | The member's hosts stay on their computer (member store); the organization's are its sealed settings. Secrets keep their own host rules. |
| Abuse and untrusted input | The host comes from an agent reply (untrusted): held to a name, never loopback, link-local or metadata, offered only where the organization lets members allow hosts, and opened only on a click that names it. The reference opens the name's addresses, so the card and the setting's help say "anything else at its address". |
| One owner | The protocol owns the rule shape; pkgmeta the settings contract; the component settings store the values; the desktop owns the machines; the bridge owns the choice of machine; find-a-way owns noticing. |
| Defaults and fallbacks | Default `memberHosts` `ask`, no hosts listed: nothing opens until a member allows one or an admin lists one. No `egressHosts` → no `allow-host` and the dialog says the runtime cannot open hosts. A package that declares no settings runs with the platform's defaults. |
| External contracts | `egressHosts` is a new optional capability with a conformance step; `public` without rules is unchanged. |
| Load on users and agents | One card when a host is blocked; the settings are in the dialog every component uses; no new tool or rule for agents. |
| Release and compatibility | Old sidecar: no capability, behaviour as today. Old desktop: never sends rules. The slice-2 desktop Settings fields are dropped without migration (pre-launch): a member who chose `all` sets it again in Components. |
| Observability | The desktop logs every apply (hosts added or removed, the machine recreated or not); the bridge logs which machine a drive ran on. |
| AI capability | Notices the block and proposes the exact host; the member decides. |
| Component or platform | Platform: the protocol capability, the machine choice, the settings contract (only the platform can enforce a boundary). The sandbox itself and its settings stay the component's. |

**Pre-mortem.**
1. *A teammate's borrowed prompt reaches the private host.* Borrowed and
   platform drives never run on `bnbloc-own`; a bridge test holds every
   drive kind to its machine.
2. *The host still does not resolve after Allow.* The rebind check is what
   blocked it; the reference adds address rules for the name's private
   addresses (§2.2), the conformance step resolves a listed private name,
   and a host whose address changes is resolved again at the next apply
   (Settings offers "Apply again").
3. *A reply talks the member into opening something.* The card names the
   host and the reach in plain words, the target cannot be loopback or
   metadata, and an organization that does not want it turns
   `memberHosts` off.
4. *The organization's values are never read and a member's host opens
   against its choice.* Until they are read, no member host opens
   (`sandbox_settings` fails closed); a desktop test holds it.

## 4. Build order (slices of §2)

1. Protocol `egressHosts` + conformance step; the reference
   implementation's rule translation and rebind admission. (built)
2. Desktop: `bnbloc-own`, apply when idle; bridge: the machine per drive
   kind. (built)
3. The sandbox's settings as its component settings (§2.3: the contract,
   the gateway's check, the desktop's reading, the dialog, the sandbox
   package declaring them) and find-a-way's `allow-host` (§2.4: the job's
   `sandboxed`, the capabilities, the card, the content). (built)

Not built: the sandbox observing which private names its guest looked up
(a protocol `egressReport`), which would let the card ask without the AI
reading the reply; it waits for a reply the AI misreads.
