# Private hosts for a member's own sandboxed agents, asked for by the AI (2026-10-21)

Status: slices 1–2 built (2026-10-21). Slice 1: `egressHosts` in the
protocol, the conformance step `egressHosts` (`SANDBOX_PRIVATE_HOST`), the
reference implementation; the full suite passes against it with an
internal host listed. Slice 2: the desktop's list (Settings → Agent sandbox
→ Private hosts, `protocol.ValidPrivateHost`), `bnbloc-own` and its apply
rule (`plan_own_machine`), the bridge's machine per drive
(`BNBLOC_SANDBOX_OWN_MACHINE`, `driveKey.ownMachine`). Slice 3
(self-configuring, §2.3 revised) next. Owner: the Sandbox Protocol (`sandbox/protocol`,
`docs/architecture/sandbox-protocol.md`), the desktop's sandbox adapter
(`apps/desktop/src-tauri/src/internal_ext/sandbox*.rs`), the bridge's
sandbox runtime (`services/bridge/sandbox.go`), find-a-way
(`docs/prd/2026-10-20-never-a-dead-end.md`), the tenant policy
(`internal/wire/tenant_policy.go`).

A member whose own agent runs in the VM sandbox can let it reach a named
private host (the team's package registry, its CI) instead of the sandbox
blocking every private address. The AI notices the block and proposes the
host; the member allows it with one click; the organization's admins
decide whether members may, and which hosts.

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

### 2.3 Self-configuring: the sandbox notices, the member allows

Revised 2026-10-21 after slice 2 (the owner: "why isn't the sandbox
self-configuring, why is it in Settings?"). The member never fills a list
first: the platform sees what the sandbox blocked, proposes it on the turn,
and one click opens it. Settings only shows what is open and takes it back.

- **The sandbox reports what it blocked.** New capability `egressReport`:
  `GET /sandbox/v1/machines/{m}/egress/blocked?sinceMs=` answers the names
  the guest looked up whose answer was a private address the egress did not
  open (name, the private addresses, first and last time, count), newest
  first, bounded. The reference implementation sees them by being the
  guest DNS's upstream: it runs a forwarding resolver on loopback
  (microsandbox's `dns.nameservers`), forwards each query to the host's
  resolvers unchanged, and records an answer holding a private address the
  machine's rules do not cover. A fact the platform observed, not a guess
  from the agent's reply text (which may leave it out, or be written by a
  prompt).
- **The bridge attaches it to the turn.** When an own sandboxed turn ends,
  the bridge reads the machine's blocked names since the turn started and
  puts them on the job (`blockedHosts`), names only. Only own drives:
  a borrowed or platform turn never carries them, and the shared machine's
  names are read for an own drive only (a borrowed turn running at the same
  time may add a name; the member still decides).
- **The card asks.** A settled own turn with `blockedHosts` shows the
  find-a-way card at once, without the gate: the fix `allow-host` for each
  name the policy lets the member open ("Your agent's sandbox could not
  reach `npm.internal.example`. Allow your own agents to reach it?"), with
  **Allow** and **Not now**. The AI part: find-a-way gets the blocked names
  in `capabilities`, explains in the summary what failed and why the host
  fixes it, and may add other fixes; with no assist model the card still
  offers `allow-host` (the fallback). Allow adds the host on the desktop
  (applied when no own turn runs there) and the card then offers **Ask
  again**.
- **Admins can pre-open hosts.** With the organization's list (§2.4) a
  listed host needs no question: the desktop opens it for every member
  whose own runs are sandboxed, and a blocked name on the list is opened at
  once.
- Nothing in the agent changes.

### 2.4 Settings and policy

- Desktop Settings → Agent sandbox → **Private hosts** shows what is open
  (the member's own and the organization's), where each came from (a room's
  card, the organization), and Remove; adding by hand stays for a host no
  run has hit yet. A host opens on any port: the reference cannot hold a
  name's port (§2.2). Shown only when the runtime advertises `egressHosts`;
  used only in mode `all` (in `borrowed` the member's own runs are on the
  host, which reaches the hosts itself).
- Organization → Settings → **Sandbox private hosts** (tenant policy,
  `manage_org`): `off` (no private host opens), `listed` (only the
  organization's list, opened for everyone without asking; the default,
  empty) or `ask` (the list opens for everyone, and a member may allow
  another host from the card), plus the list.
- `GET /v1/my/tenant-policy` carries both, so the desktop and the card read
  one answer. A member's host the policy no longer allows is dropped from
  the machine at the next apply, and the Settings row says why.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member decides for their own agents; admins bound it; a borrower and a platform drive never reach a private host (§2.1). An agent never adds a host: the member's click does. |
| Time and lifecycle | A change applies when no own turn runs (never-stop-live-runs); a recreate takes under a second; a host the policy drops is removed at the next apply. |
| Scale and cost | A second VM only for members with private hosts; idle cost is memory (paused when idle is a follow-up). |
| Failure and recovery | `bnbloc-own` not ready → own drives run on `bnbloc` as today (the host just stays blocked); the card says the change did not apply and why. An unreadable policy counts as `off`. |
| Boundaries and privacy | The host list stays on the member's computer; the tenant policy is the only server state. Secrets keep their own host rules. |
| Abuse and untrusted input | The host comes from an agent reply (untrusted): held to a name, never loopback, link-local or metadata, bounded by the organization's list, and opened only on a click that names it. The reference opens the name's addresses, so the card and Settings say "and anything else at its address". |
| One owner | The protocol owns the rule shape; the desktop owns the machines and the list; the bridge owns the choice of machine; find-a-way owns noticing. |
| Defaults and fallbacks | Default policy `listed` with an empty list: nothing opens until an admin lists a host or switches to `ask`. No `egressHosts` → no Private hosts section and no `allow-host` action; no `egressReport` → no `blockedHosts`, the card runs gated as today. |
| External contracts | `egressHosts` is a new optional capability with a conformance step; `public` without rules is unchanged. |
| Load on users and agents | One card when a host is blocked; no new tool or rule for agents. |
| Release and compatibility | Additive. Old sidecar: no capability, behaviour as today. Old desktop: never sends rules. |
| Observability | The desktop logs every apply (hosts added or removed, the machine recreated or not); the bridge logs which machine a drive ran on. |
| AI capability | Notices the block and proposes the exact host; the member decides. |
| Component or platform | Platform: the protocol capability, the machine choice, the policy (only the platform can enforce a boundary). The sandbox itself stays a component. |

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
   metadata, and the organization's list bounds it by default.

## 4. Build order (slices of §2)

1. Protocol `egressHosts` + conformance step; the reference
   implementation's rule translation and rebind admission. (built)
2. Desktop: the member's list, `bnbloc-own`, apply when idle, Settings;
   bridge: the machine per drive kind. (built)
3. Self-configuring: protocol `egressReport` + conformance step, the
   reference's observing resolver; the bridge's `blockedHosts` on own
   turns; find-a-way's `allow-host` action, the blocked names in
   `capabilities`, the card without the gate; tenant policy (`off` /
   `listed` / `ask` and the list), Organization settings, the
   organization's hosts opened by the desktop.
