# Agent manager: install and update the agents on a member's computer (2026-10-24)

Status: first version built 2026-10-24 (§7): the manager's engine and page, the
ACP core's `agents.d`, and the platform's plugin panel (§2.6, §7). Not yet run
inside the real desktop app, and not published. Owner: a new desktop plugin component, the **agent
manager** (`github.com/luckylhb90/bnbloc-agent-manager`, its own repository:
the catalog, the install and update engine, the member's page). Two small
changes outside it: the ACP core reads a drop-in directory of agent entries
(its own repository), and the platform adds one generic mechanism, a plugin's
own **panel** (§2.6). Nothing in the platform names agents, registries or
package managers.

A member's own agent (Claude Code, Codex, Gemini CLI, OpenCode, …) is a program
on their computer. Today they find it, install it with whatever tool they know,
write its launch line into `agents.json` by hand, and nobody tells them when it
is out of date. The agent manager does those three things from one page:
**see** which agents are installed and at what version, **install** one,
**update** one, and have the ACP core pick it up without editing a file.
Nothing is installed or changed until the member clicks, unless they turned
automatic updates on.

It manages **the common coding agents as one list, ACP or not**. Managing
(detect, version, install, update) and **registering with the ACP core** are
separate steps: an entry that has a launch line for ACP is also registered, an
entry without one is still listed and kept current, and the row says it cannot
run in a room yet. An agent the member installed themselves (by the vendor's
installer, a package manager, npm) is managed too, through the update method the
catalog names for how it was found (§2.3), because that is where most members'
agents are.

## 1. Why

- The ACP core's own README starts with "Configure your agents": a JSON file,
  a launch command per agent that "changes between agent releases", and an
  agent that is not installed is listed unavailable with a reason the member
  must act on alone (docs/prd/2026-10-11-acp-core.md). A member with no
  terminal habit is stopped at the first step; one with the habit runs agents
  of any age.
- The dead-end card (docs/prd/2026-10-20-never-a-dead-end.md) can say "use your
  own agent" and the Agents page then has nothing to offer a member who has
  none.
- Agents move fast and break compatibility between releases (launch flags,
  ACP revisions, session storage). The organization has no way to say which
  versions its members should run, and a member has no way to go back.
- Installing software is the one thing here with real risk: it runs code on a
  member's computer. That is why the design is mostly about **who is trusted to
  name what gets installed** (§2.3, §4).

## 2. What it is (the end state)

### 2.1 The parts

```
 member's page (a bundle, in the component's card → Open)       §2.2
        │  host messages only; no network, opaque origin
        ▼
 desktop: plugin panel host + relay (loopback call, per-launch bearer)   §2.6
        │
        ▼
 agent manager sidecar (member's computer, the member's user)
   catalog (signed, in the package)  →  detect / check / install / update / remove
   store: <data dir>/agents/<id>/<version>/  +  state + operation log     §2.4
        │ writes, atomically
        ▼
 <acp home>/agents.d/<manager>.json   →   ACP core (reloads on change)    §2.5
        │
        ▼
 the agent appears as a target (`acp/<name>`); sign-in is the agent's own
```

### 2.1a Placement: content, component or platform

| Part | Placement | Why | What an organization would swap |
|---|---|---|---|
| Catalog: which agents exist, how each is detected, installed, updated, launched, which sign-in hint it carries | **Component** data, in the signed package | It decides what runs on members' machines, so it is signed like code and versioned with the component. A fact about the outside world (a package name, a version flag), never platform | Its own build of the manager with its own catalog (the Replace test) |
| Detect, check, install, update, roll back, remove; the typed drivers (`npm`, `release`); the store and the operation log | **Component** (desktop plugin sidecar) | Its own runtime and external systems (registries, release hosts) | A second manager beside it |
| Policy: allowed agents, pinned versions, how young a version must be, automatic updates | **Component settings** (existing) | Declared `settings`, scopes `org` and `member` (docs/prd/2026-10-19-component-settings.md): no new mechanism | The values |
| Registering an installed agent with the ACP core | **ACP core component**: `agents.d` | The schema is the core's configuration; its owner changes it (§2.5) | Another core reads its own |
| The member's page | **Component** (a bundle the package pins) | The look and the steps are the component's | A different page in a different build |
| A plugin's own panel: package field, sandboxed frame, the relay to the sidecar's loopback | **Platform** | Confinement (the frame has no network and an opaque origin) and identity (the bearer stays in the desktop, never reaches the web); the bundle is signed and pinned. Named reasons: signing and confinement | Nothing: a mechanism any plugin uses |
| Diagnosing a failed install | **Content**: an assist (`diagnose-agent-install`) | The assist seam exists; the prompt is content (§2.7) | The assist, in the Library |
| Consent, signing, rollout, tenant scope, the kill switch for the component | **Platform** (existing) | Installing it is the consent; `optin` rollout | None |

Platform tests: *Name*: the platform gains "panel", never "agent", "npm" or a
product. *Fork*: an organization with its own agents publishes its own build;
nobody patches the platform. *Replace*: two managers may be installed; each
writes its own drop-in. *Reason*: signing and confinement, above.

### 2.2 What a member does

1. Components → **Agent manager** → Install (an `optin` component), then **Open**.
2. The page lists the catalog: each agent with its state: *not installed*,
   *installed 1.2.0 (managed)*, *update available 1.3.0*, *installed by you
   (1.2.0)*, *working…*, *failed: reason*, *can't check (offline)*.
3. **Install** puts the agent in the manager's store and registers it with the
   ACP core. The row then shows the agent's sign-in hint from the catalog
   ("run it once and sign in"): each agent keeps its own sign-in, the manager
   never reads it. The agent appears in the member's Agents page as a target
   within a minute (the ACP core reloads and probes).
4. **Update** installs the new version beside the old, checks it, then switches
   the registration. **Roll back** switches it back to the previous version
   (kept). **Remove** deletes the agent's versions and its registration.
5. An agent found on the computer that the manager did not install is listed
   with its version and **how it was found** (the vendor's installer, a
   package manager, npm global, an unknown place). **Update** on it runs only
   the catalog's typed update method for that kind of install (§2.3), on the
   member's click, showing the exact command first; an unknown place is read-only
   with the entry's hint. The manager never **removes** what it did not install,
   and never runs an update that needs administrator rights. If the member's own
   `agents.json` already defines that name, the row says a managed copy would not
   be used.
6. Settings (Components → Settings): the member's `autoUpdate` (`off`
   default, `on`), and the organization's `allowed` (agent ids, empty = all of
   the catalog), `pinned` (`id@version` lines) and `holdDays` (a version
   younger than this is not offered). A pin is the version the page offers and
   installs; a member may still install nothing else of that agent.
7. A failed operation shows the last lines of its log and **Explain** (the
   assist, §2.7).

Nothing happens in the background but the check for newer versions (on opening
the page and every six hours while the component runs) and, with `autoUpdate`,
the update itself.

### 2.3 The catalog: who is trusted to name what gets installed

The catalog is data in the package (`catalog.json`), signed with the
package, so only a published component version changes what the catalog itself
may install or run. Two settings add agents beside it without a new build, and
they differ in whom they may make run a command (`manager/config.go`):

- **A member's own** (`myAgents`, kept on their computer, never sent anywhere):
  whatever their own computer can run: a command name or an absolute path, version
  arguments, an updater and its arguments, an ACP launch line. They could run it
  themselves, and the page shows an update's exact command before the click. A
  line with the id of a catalog agent and only `command` and `versionArgs` points
  that agent at another command or path.
- **An organization's** (`orgAgents`): recognition only: a bare command name, one
  version flag from `--version`, `-v`, `-V`, `version`, and words for the member.
  **No update command, no ACP launch line, no change to a catalog entry**: a
  setting must never make every member's computer run what an admin typed, which
  no organization setting does today (installs and rollouts of components are
  super-admin acts). An organization that needs entries that run commands makes its
  own build of the manager (`Catalog.With`, the `app` package) and signs it as its
  own package, which is a second manager beside the public one (the Replace test).
  It can still narrow the catalog (`allowed`, `pinned`, `holdDays`), configured
  agents included.

A line that does not fit is named on the page and left out.

An entry is typed, never a shell string:

```json
{
  "id": "example-agent",
  "name": "Example Agent",
  "platforms": ["darwin", "linux", "windows"],
  "install": [
    { "method": "npm", "package": "@example/agent-acp", "bin": "example-agent-acp",
      "ignoreScripts": true, "requires": [{ "tool": "node", "min": "20" }] }
  ],
  "latest": { "from": "npm" },
  "detect": { "command": "example-agent-acp", "args": ["--version"] },
  "register": { "name": "example", "args": [],
                "profile": { "instructionsVia": "file:AGENTS.md", "skillsDir": ".agents/skills" } },
  "signIn": "Run `example` once and sign in."
}
```

The example is illustrative. A real entry's launch line, version flag and
profile mapping are probed against the real agent when written (the facts
"known to work" now in the ACP README move into the catalog, one owner), never
assumed.

The drivers are code in the component, each one verifiable, which is why there
is no "script" method:

- `npm`: `npm install --prefix <version dir> <package>@<version>` with the
  manager's own npm cache and prefix (the member's global packages and cache are
  untouched), HTTPS registries only; the version comes from the registry
  (`dist-tags`, publish time for `holdDays`) and is checked as a version string
  before it becomes a directory name.
- `release`: a release asset from a host the entry names (a repository and an
  asset pattern per platform), downloaded over HTTPS, no redirect to another
  host, its sha256 checked against the release's checksum file or the entry's pin;
  a mismatch installs nothing.
- **Update-only methods for an install the manager did not make**, each typed
  and matched to how the agent was found (by its path and the catalog's
  `foundBy` rule): `self` (the agent's own update subcommand, args fixed in the
  entry), `brew` (`brew upgrade <formula>`), `npm-global`
  (`npm install -g <package>@<version>`). They change the member's own
  environment, so they run only on a click, after showing the exact command,
  and the result is verified by `detect` like any update; a failure is reported
  and nothing is retried silently. There is no rollback for them (the manager
  did not keep the old version): the row says so before the click.
- **A check may come from the agent itself** (`latest: {"from": "self", "args":
  [...]}`, e.g. Hermes's `update --check`), when the agent knows its own channel
  better than a registry does; the output's format is the entry's parsing rule,
  probed, never guessed. An entry's `pinnable` says whether a version can be
  targeted at all (`npm` and `release`: yes; a channel-following `self`: no),
  which is what lets a pin be enforced instead of silently ignored.
- An agent whose only official channel is a script installer is not installed by
  the manager (no `script` method). If it publishes release assets the `release`
  driver installs it; otherwise the entry carries `detect` and an update method
  and the row's hint says how to install it first.
- Never elevated: a method that needs administrator rights is refused with the
  reason; the manager never asks for or stores a password.
- The tool prerequisites (`requires`) are looked up in the same login-shell
  environment every desktop child gets (`shell_env.rs`), so a Node from a version
  manager is found. A missing one is the row's state, with the entry's hint.

`platforms` lists where the agent runs at all; on any other the row says so
instead of offering an install that cannot work. `ignoreScripts` (npm) is an
entry field, **true unless the entry says otherwise**: an upstream's own install
instructions may ask for it, and a package that needs its install scripts says
so in the entry.

### 2.3a The first batch

What is known about each agent from its own documentation or source, read
2026-10-24. **None is probed on a real install yet** (§6 item 7): the table says
what each entry will be built from, not that it works.

| Agent | Where found | Install by the manager | Update | ACP | Platforms and needs |
|---|---|---|---|---|---|
| Claude Code | the command the member installed (`claude`; npm `@anthropic-ai/claude-code` names the newest version) | its own installer (hint only) | by how it was found: the vendor installer's `claude update` | none itself; **its ACP adapter is its own row** (`@agentclientprotocol/claude-agent-acp`, `npm`, registered for rooms) | Node ≥ 22 for the adapter |
| Codex | the command the member installed (`codex`, npm `@openai/codex`) | `npm` | `npm` | none itself; **its ACP adapter is its own row** (`@agentclientprotocol/codex-acp`) | Node |
| Gemini CLI | the ACP README: `gemini --acp` | `npm` (to confirm) | `npm-global`, or its own | `--acp` | Node |
| OpenCode | the ACP README: `opencode acp` | to probe (its channels) | its own, or `brew`/`npm-global` | `acp` subcommand | to probe |
| **pi** | its docs page (`packages/coding-agent` of the pi repository) | `npm`: `@earendil-works/pi-coding-agent`, `ignoreScripts`; the docs name a script installer, a Windows one and Nix too | `pi update` for the installer's setup (`self`); `npm` for ours | none documented, so **listed and kept current, not registered** | macOS, Linux, Windows; Node ≥ 22.19 (its installers can set Node up; ours does not) |
| **omp** | `omp --version` (omp.sh; the Homebrew formula `can1357/tap/omp`) | Homebrew (hint only: no `brew` install method) | a Homebrew install runs `brew upgrade can1357/tap/omp` on a click (an update entry may name another bare command); `omp update --check` reports a newer version than the formula carries, so no newer version is claimed | built in: `omp acp` | macOS, Linux |
| **Hermes Agent** | its README and install and update docs (hermes-agent.nousresearch.com), read 2026-10-24 | **none typed.** Its channels are script installers (`install.sh`, and `install.ps1` for native Windows; no admin rights, user directories), signed desktop packages (a DMG on Apple Silicon, an MSIX on Windows) whose updater owns them, a Termux package repository, Docker and Nix (no longer supported). No package-index or release-asset install is documented | by how it was found: a **source install** (the scripts' managed checkout: `~/.hermes/hermes-agent`, `%LOCALAPPDATA%\hermes\hermes-agent`) runs `hermes update` (`self`), checked with `hermes update --check`; a desktop package, Docker, Nix or Termux install is **updated by its own owner**, and `hermes update` itself declines on those, so the row is read-only and names the owner | built in: `hermes acp` (also the `hermes-acp` script) | macOS, Linux, WSL2 and **native Windows** (the local checkout read earlier said otherwise: it was stale); the version is `hermes --version` |

What the batch changes in the design, all of it already in §2.3 or above:

- **Hermes is the case the "no script" rule was written for, and its newest
  documentation does not change that.** Every way to install it is a script or a
  GUI package. The member installs it themselves once (the row offers the
  catalog's install hint, shown not run); from then on the manager finds it, says
  how it was found, checks, updates a source install with `hermes update`, and
  registers its ACP line. Three things in its update docs shape the entry:
  (1) `hermes update` **restarts the member's running gateways** unless given
  `--no-gateway-restart`, which the entry's fixed arguments always include (a
  running agent is never stopped); (2) it updates in place, with its own backup
  and its own rollback on a failed syntax check, so the manager's side-by-side
  staging does not apply and the row says there is no manager rollback (and the
  docs call a rollback after the new version wrote its state unsupported);
  (3) it follows a **channel** (stable by default, `main`, a desktop canary) and
  has no "install version X", so an organization's `pinned` cannot be honored
  for it: such an agent is marked `pinnable: false` in the entry and, under a pin,
  its Update is disabled with that reason, never run unpinned. The manager never
  switches a member's channel. Making Hermes installable by a click is a
  decision, not a given (§6 item 8).
- **pi is a plain `npm` agent with no ACP.** It is the first listed-not-registered
  row, and it has two update routes by how it was found: the installer's `pi
  update`, or `npm`. Which one applies is `foundBy`, from the real install's
  paths.
- A package name is a fact that moves (pi's documentation shows a different scope
  from older write-ups): a name that no longer resolves is a row state, "the
  catalog's package was not found", never a different package guessed. The catalog
  is maintained like any dependency list.

### 2.4 The store, an update and what it never does

`<data dir>/agents/<id>/<version>/` holds one installed version; `state.json`
(owner-only) records, per agent, what the manager installed (version, method,
when, the previous version kept) and the last check. The operation log is a
bounded ring per agent; commands are logged without their environment, never a
credential.

- **An update is staged, verified, then switched.** The new version installs in
  its own directory (marked partial until done); the catalog's `detect` is run
  from it and must report that version; only then the registration entry is
  rewritten (temp file and rename). Any failure before that leaves the working
  version and its registration untouched; the half-made directory is swept at
  the next start. There is no moment where the registered agent is half
  installed.
- **Versions side by side, no links.** The registration names the exact version
  directory, so there is no "current" link to swap (a junction on Windows) and a
  rollback is the same rewrite. The previous version is kept; older ones are
  pruned when nothing runs from them (a delete that fails because a process uses
  it is retried later, never forced).
- **A running agent is never stopped.** A process that runs from the old version
  keeps running; the new one is used by the next launch. The component never
  signals or kills an agent process.
- **One operation per agent at a time.** An operation ends when it shows no
  activity (no output, no bytes) for the stall window; there is no total time
  cap (an agent's first download can be about 0.5 GB). Cancelling is the
  member's button; the partial directory is then swept.
- **No sudo, nothing outside the store and the registration file**: no edit to
  the member's `PATH`, shell profile, global npm prefix or `agents.json`.
- **Offline or a failed lookup is "can't check"**, never "up to date".

### 2.5 Registration: the ACP core's drop-in directory

The ACP core reads `<home>/agents.json` today. It gains `<home>/agents.d/*.json`:
each file holds `agents` entries of the same schema, read in name order, reloaded
when the directory or any file changes, an invalid file skipped and logged
while the others and the previous good copy of that file stay. The member's
own `agents.json` wins on a name collision, so a hand-written line is never
overridden; a collision is reported in `bnbloc-acp check`. The manager writes
only its own file (`agents.d/agent-manager.json`), each entry's command being the
absolute path inside the version directory. One owner for the schema: the ACP
core's `config.go`; the manager imports nothing of it and writes the documented
shape.

Releasing in order matters: an ACP core older than this ignores `agents.d`, so
the core ships first (harmless alone), the manager after it.

### 2.6 The platform's one addition: a plugin's panel

A plugin that has something to show but no hook has no neutral way to show it
today: the only seams are named for one use (`skills-provider` renders the
skills view; `settingsPage` edits settings and nothing else). The platform adds
the generic one:

- **Package.** A plugin may declare `panel: {entry, bundle}` (the shape of
  `settingsPage`: a bundle pin, an `.html` entry, validated at install by one
  owner next to `core/pkgmeta/settings_page.go`). A plugin with a panel and no
  hook is valid (the way a capability waives the hook rule now), and it needs
  `sidecar.tokenEnv`, the bearer of its loopback API.
- **Frame.** The Components card of such a plugin has **Open**; the bundle is
  served by the previewer code under the same sandbox as a settings page
  (opaque origin, no network, no `fetch` of its own).
- **Relay.** The frame sends `{call: method, path, body}` to its host; the host
  invokes one desktop command that forwards it to **that plugin's** loopback URL
  with the launch bearer. Only paths under `/v1/`, a fixed set of methods, a
  body bound, an answer bound, a timeout; the bearer never leaves the desktop.
  The existing relay of the skills provider (`skills_provider.rs`) is the code to
  generalize: one relay function, its commands thin callers (one owner;
  `TestPluginRelaySingleOwner` forbids a second).
- **Not built here:** pushing events to the frame. The page polls the
  sidecar's `GET /v1/state` while open (a second while an operation runs).

The skills view is not migrated in this record: it publishes to the library
through the web's own import, which a sandboxed frame cannot do. Whether it ever
moves onto the panel is a separate decision (§6).

### 2.7 AI

The one decision in this feature is a failure, not an install: *why did it fail
and what should I do?* The member presses **Explain** on a failed row; an assist
(`diagnose-agent-install`, content, in the default-content repository) gets the
catalog id, the OS and architecture, the versions of the tools the entry
requires, and the last sixty log lines (redacted by the component before they
leave it: no environment, no home path), and answers `fixes: {action, why}`
items (install Node 20, check the proxy, free disk, …). It is the member's
own budget and the organization's switch; AI proposes and the member decides:
no fix is applied by it. With the assist off the row shows the log tail and
the entry's hint, and the manager works the same.

Detect, check, install, update and roll back carry no decision, so no model
is in them (LESSONS "Take the model out of a step that has no decision in it").
An agent in a room never gets a tool to install agents: installing software is
never an agent's act (no new tool, no new rule).

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member (their computer, their agents), an organization admin (policy only), a super-admin (publishes the catalog build), the second account on the same computer (a plugin runs per account, the ACP core once per computer: docs/prd/2026-10-22-multi-account.md §2.4), a borrower (never: their runs use the owner's agent, which the owner manages), an agent (cannot install: no tool). Each account's manager keeps its own store and its own drop-in file; open item 1 for the shared ACP core |
| Time and lifecycle | Update while a run is going: staged beside, the registration switches, the running process keeps its old files (never signalled); the next launch, including a resume after the idle stop, uses the new one. Quit or crash mid-install: partial directory swept next start, registration untouched. Sign-out stops the sidecar and any install with it. Uninstalling the component removes its data dir, store included: the confirm says so, and its drop-in is removed on stop by the manager (open item 1). A session resumed by a newer agent version is the real risk (open item 2) |
| Scale and cost | Catalog tens of entries; per agent two versions kept (the Claude adapter is ≈0.5 GB, so ≈1 GB): the page shows each size and offers prune; the manager's own npm cache is bounded by pruning with its versions. Update checks: one lookup per agent per six hours, only the registry host |
| Failure and recovery | Each driver failure is a row state with words: tool missing (and which), network, checksum mismatch (nothing installed), no space, permission, an agent that installs but does not report the version (not registered). Retry is safe: staging is idempotent and never touches the working version. A failed check never marks "up to date" |
| Boundaries and privacy | The frame has no network; the bearer stays in the desktop; the relay reaches only the plugin's own `/v1/`. The manager never reads an agent's sign-in or config, and logs no environment. The registry sees the member's IP and the package asked for, as when the member runs it themselves; the organization's proxy and CA settings apply (the same environment the desktop gives every child). Nothing is sent to the gateway by the component |
| Abuse and untrusted input | The catalog is signed; no entry is added by a setting; no shell strings; version strings from a registry are validated before they name a directory; a redirect to another host is refused; an upstream package's own install scripts are the agent's trust (as when the member installs it), narrowed by `allowed`, `pinned`, `holdDays` and off-by-default automatic updates. Log text shown to the member and sent to the assist is untrusted text: fenced in the assist input, never an instruction |
| One owner | Schema of an agent entry: the ACP core. Settings: `core/pkgmeta/settings.go`. The panel bundle's validation: with `settings_page.go`. The relay: one function. Catalog facts (launch lines, profile mappings): the catalog, removed from the ACP README table |
| Defaults and unknowns | Nothing installed, no automatic updates, until the member chooses. "Detection still running" is a state, never "not installed". An org pin newer than the registry's latest is offered as is; an unknown tool version is "can't tell", not "too old" |
| External contracts | Registries' and release hosts' answers (dist-tags, publish times, assets, checksums), each agent's version flag and ACP launch line: probed against the real thing for every catalog entry before it ships; the drop-in reload against the real ACP core; the panel's relay against a conformance fixture plugin. Fakes must refuse what the real side refuses (a bad checksum, a registry 429) |
| Load on users and agents | One page, one row per agent, one button per state; four settings. Agents see nothing new: no tool, no prompt line |
| Release and compatibility | ACP core with `agents.d` first (auto, inert alone). The panel seam is additive (a new optional package field; older desktops cannot open it, and the card says the desktop must update). The manager ships `optin` and stays so (it installs software); the first run is verified on the member's own desktop across a manager upgrade with an agent installed and a run going |
| Observability | Component and catalog versions in the page and the log; each operation's start, end, reason in the ring and the component log (warn on failure); `bnbloc-acp check` shows the collision and the reason an agent is unavailable. The check that matters end to end: install on a clean profile, see the target, run a turn, update, run a turn |
| AI capability | The failure assist (§2.7); no AI in the steps with no decision; works without the model |

**Pre-mortem.** It shipped and a month later it failed badly:

1. **An update broke a member's working agent** (a launch flag changed, an old
   session cannot be read by the new version). Defence: the previous version
   stays registered until the new one has passed `detect`; one-click roll back;
   automatic updates are off by default; the organization can pin and hold;
   open item 2 is closed before `autoUpdate` may become `on` by default.
2. **Installs fail in the field and the member is stuck** (no Node, a
   corporate proxy that re-signs TLS, a read-only home, Windows paths, a slow
   download that looks stalled). Defence: the shell environment and the
   proxy/CA settings the desktop already gives children, prerequisites named in
   the row, log tail plus **Explain**, a stall window instead of a total cap, no
   sudo, a Windows run in the verification (one platform per slice, none
   skipped).
3. **A bad upstream release or catalog reaches many machines.** Defence: the
   catalog is signed and the component is `optin`; no auto update by default;
   `holdDays`, `pinned`, `allowed`; checksums on the `release` driver; the
   previous version one click away. There is no fleet view (§5), so a bad
   version is found by members, not by an admin: the reason `pinned` exists.
4. **Two sources of truth for an agent** (the member's own `agents.json`, the
   manager's drop-in) confuse people. Defence: the member's file always wins, the
   page says so on the row, and the manager never edits what it did not write.
5. **An update of the member's own install breaks their setup** (a brew or npm
   update changes what they scripted against, or needs rights it lacks).
   Defence: only on a click, the exact command shown first, never elevated,
   verified after, and the row says there is no rollback for an install the
   manager did not make; `pinned` applies to these too.

## 4. Build order (slices of §2, each survives unchanged)

The manager's engine does not depend on the other two; they meet at the end.
1. **The manager engine** (`bnbloc-agent-manager`): catalog, detection of how
   an agent was found, the `npm` driver and the update-only methods, store,
   state, operation log, settings, driven by its own loopback API and tests;
   two real agents installed, updated and rolled back, one found already
   installed and updated, on macOS; then Windows and Linux.
2. **Platform: the plugin panel** (this repository), in parallel: the package
   field and validation, the frame host, the one relay (the skills commands
   call it), a conformance fixture plugin;
   `docs/architecture/plugin-panels.md`. The manager's page rides on it.
3. **ACP core: `agents.d`** (its repository), in parallel and released first
   of the three (inert alone): the directory, the merge rule, `check` showing
   collisions; then the manager's registration step.
4. **The catalog's breadth**: one entry at a time, each probed against the real
   agent (version flag, how it is found, its update method, its ACP launch
   line if any); the `release` driver with the first release-hosted agent.
5. **The assist** `diagnose-agent-install` in the default-content repository,
   with its golden and budget tests.

## 5. Not in this record

- **Fleet visibility for admins** (which versions run on whose computers).
  A component on a member's computer has no way to report to a service today;
  designing one is its own record.
- **Removing what the manager did not install.**
- **Installing the tools an agent needs** (Node, Python).
- **A badge outside the page** (the Agents page, the dock): needs a way for a
  plugin to say "attention" that is not named for one use.
- **Running a non-ACP agent in a room.** The manager lists and updates it; a
  room needs a core that speaks to it (an adapter, ACP or another).

## 6. Open items and what was not verified

1. **Two accounts, one computer.** A plugin runs per account; the ACP core once
   per computer reading one home. Not verified: where each account's drop-in
   file lives, which account's agents the shared core serves, and what
   uninstalling the manager does to the files it wrote (the desktop sweeps the
   plugin's data dir; a drop-in outside it would dangle). Recommendation: one
   drop-in file per account instance, entries namespaced by account, the manager
   removing its file on stop and uninstall, the sweep removing the store; to be
   checked against `shared_core_tests.rs` and the multi-account record before
   slice 3.
2. **A session resumed by a newer agent version.** Not verified for any agent.
   Test: start a session on version N, update to N+1, resume after the idle
   stop. Blocks a default of `autoUpdate: on`.
3. **A sandboxed run of a managed agent.** The ACP sandbox recognises the
   routing from the agent's command ("also by an npx or another path"); not
   verified for an absolute path inside a version directory, nor that the
   confinement can read and execute the store. Slice 3 checks it with the real
   helper (docs/prd/2026-10-22-acp-sandbox.md §2.3); until then a managed agent's
   row says whether sandbox mode `all` can run it.
4. **Org-added catalog entries** are refused (§2.3). If an organization needs
   its own agent without publishing its own manager build, the way is a signed
   per-tenant catalog add-on, which needs a signing seam that does not exist:
   not proposed.
5. **Defaults to decide:** `holdDays` (proposal: 2), the check interval (six
   hours), the stall window.
6. **Whether the skills view later moves onto the panel** (§2.6): leaving two
   UI seams is a cost; the panel cannot yet publish to the library.
7. **Every catalog entry's real facts** (version flag, launch line, profile
   mapping, requirements, how an existing install is recognised) are probed per
   agent at build time; none in §2.3 or §2.3a is a verified fact. The local
   Hermes checkout was stale (it said no native Windows and a git-only update);
   the docs read afterwards say otherwise, which is the reason for probing.
   Known gaps: pi's documentation names no `--version` flag and no ACP; Hermes's
   `--check` output format and whether `hermes update` runs without a prompt
   (`--yes` is not documented for it; without a terminal it stashes local edits)
   are not checked.
8. **Whether Hermes becomes installable by a click.** Options: (a) the install
   hint only, the member runs the installer (this record); (b) a `script`
   driver limited to the host the catalog names, run non-interactively with the
   command shown first. (b) runs whatever that host serves at that moment, which
   the signed catalog does not pin; Hermes's installer itself fetches pinned,
   verified parts, but may call `sudo -n` for a missing system library on Linux,
   which the manager's "never elevated" cannot prevent from the outside. Not
   proposed without the owner's decision.

## 7. As built (2026-10-24, first version)

Built: `github.com/luckylhb90/bnbloc-agent-manager` (engine, API, command line,
the page, package declaration; tests with a fake npm and a fake registry, under
`-race`), and `bnbloc-acp`'s `agents.d` (`dropins.go`, with tests). Verified on
macOS with the real thing: the registry lookup for the six agents, a real
install of pi and of Gemini CLI (version checked, registration written), an
update to a pinned older version beside the newer one, rollback and remove, the
found OpenCode and Hermes on the author's computer recognised, and the real
`bnbloc-acp check` reading the manager's drop-in and launching the managed
Gemini over ACP (it stopped at the agent's own sign-in, as designed).

Where the build departs from the text above:

- **Registration of an agent found on the computer** needs the member's
  "Use in rooms" click (an install by the manager registers, since the install
  was the click). Finding an agent registers nothing.
- **Install is not offered next to an install the member made**: Update is, by
  that install's own method.
- **`latest.from: self` and the `brew` method are not built**; the catalog
  validator refuses them. Hermes has no known latest version until one is
  probed, so its row offers "Run its updater" without claiming a newer one.
- **`agents.d` sits beside the config file** (`dirname(--config)/agents.d`),
  which is `<home>/agents.d` by default.
- **The plugin panel (§2.6) is built** as `docs/architecture/plugin-panels.md`.
  It reuses what a settings page already had: the page declaration and its one
  validation (`validatePage`, the type renamed `ComponentPage`), the pin among
  `Package.Pins`, the install probe (`probePages`), the artifact entitlement,
  the previewer's frame and its desktop scheme (the segment `_settings` became
  `_page` for both), `--panel` beside `--settings-page`, and the loopback call
  the skills provider already made (`call_raw`, now shared). New: the `panel`
  field, `Adapter::plugin_panel_call` with `panel_request` as the one rule for a
  call's path and method, `panelProtocol.ts` and the SDK's `panel`, the host
  (`panelFrame.ts`, `PanelDialog.tsx`). The platform names no use for it and no
  agent, registry or package manager (the Name test: grep finds only "panel").
  The messages are `panel.ready`, `panel.init`, `panel.call`, `panel.result`,
  `panel.refused`, `panel.close` (this record's earlier `bnbloc.panel.*` names
  were replaced by them); the page is `manager/page/` (a classic script file, as
  the frame's policy allows no inline script).
- A plugin that declares a panel gets a bearer and a data dir; the bearer
  condition is "skills-provider or panel", not "any tokenEnv".
- **Verified for the panel:** the real manager package parses under the
  platform's validator (4 settings, the panel, the bearer) and is refused until
  its bundle is uploaded; the gateway installs a plugin with a panel and refuses
  a placeholder, a non-archive and a missing entry; the member catalog lists it
  and the member may download the bundle (not the binaries); the relay's rules
  and its keeping of a failure's status (Rust tests); the host and SDK protocol
  (node tests); and the real page in a `sandbox="allow-scripts"` frame, driven
  only by `panel.*` messages, showing real data (headless Chrome). **Not run:**
  the desktop app itself (the scheme serving the bundle by pin, the Tauri
  command, the card's Open), and a publish to a gateway with a real signature.
- Not verified and not built: the real desktop app opening the panel (see above), Windows and Linux runs (the code is vetted for
  both; the fake-npm tests skip on Windows), the sandbox question (§6 item 3),
  two accounts on one computer (§6 item 1), a session resumed across an update
  (§6 item 2), `hermes update` and `pi update` on real installs, and the
  `release` driver and the assist (slices 4 and 5).

### 7a. After the first trial (2026-10-24)

- **An agent and its ACP adapter are two rows** (`for: <agent>` in the catalog):
  the first batch looked for the adapters' commands, so the `claude` and `codex` a
  member had installed showed as not installed. The agent is the command the
  member knows; the adapter is its own entry under it.
- **omp** (a separate agent, not pi) joined the catalog; **pi** stays "not
  installed" where no `pi` command is on the PATH (a copy private to another tool
  does not count).
- An update entry may name **another bare command** (`update.command`), run with
  the entry's arguments (a package manager), shown before the click.
- **Configurable agents** (above) replaced the idea of an internal build for each
  organization's agents: an agent that lives in an unusual place, or an internal
  command, is a line of configuration. Hardcoding it in the public catalog would
  also put an internal name in a public repository.
- **`npm-global` means a global prefix** (`…/lib/node_modules/`,
  `…/npm/node_modules/`): a `node_modules` that is another tool's own copy is not
  one, and `npm install -g` would not update it.
