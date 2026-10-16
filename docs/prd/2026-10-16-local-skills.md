# Local skills: a member's own skills in the sandbox, and in their library (2026-10-16)

Status: implemented (2026-10-16) except the end-to-end check on a real sandboxed
run (milestone F); revised the same day to make it an installable component
behind two small contracts. How it is built: `docs/architecture/local-skills.md`. Builds on `docs/prd/2026-10-09-agent-vm-sandbox.md`
(the hybrid execution: the agent CLI on the host, its shell commands in one
Linux VM), `docs/architecture/sandbox-protocol.md` (spaces and mounts),
`docs/architecture/content-components.md` (skills as content) and
`internal/contentpkg/import.go` (importing a skill).

## 1. Why

A member keeps skills on their machine. Today's open standard puts them in one
place, `~/.agents/skills/<name>/SKILL.md`, and every agent CLI reads that
directory or a symlink to it from its own (`~/.claude/skills/<name>`,
`~/.config/opencode/skills/…`). Two things do not work for them:

- **In the sandbox, a skill's scripts and files are not there.** In a sandboxed
  run the CLI stays on the host, so it reads `SKILL.md` and loads the skill as
  usual; the commands it then runs (`python ~/.agents/skills/x/scripts/y.py`)
  run in the guest, which sees only the room's workspace share. The command
  fails with "no such file". (A run that is not sandboxed, the default for a
  member's own drive, reads the directory directly and is fine.)
- **A skill cannot reach anyone else's agent.** A teammate's agent, an agent on
  another machine, a server-side sandbox: none has the member's skills unless
  they are content components. Making one means building an archive by hand and
  importing it in the Library, with no check that it holds no secret.

What does not need fixing: the platform already delivers skills to every kind of
agent (each core's `agentProfile` writes them where that agent reads them, or
under `.agents/skills/`). Supporting another agent is not part of this design;
only where a member's skills live, and where they must be visible, is.

## 2. Rules

1. **Nothing is shared by default.** A fresh install lists no skill and mounts
   none. The member turns the feature on in Settings → Skills (desktop app only),
   adds the folders to read, and ticks the skills to use. A folder that is
   usually there (`~/.agents/skills`) is offered as a one-click "Add", never
   added for them.
2. **Sources are folders the member names.** A source is a folder of skill
   folders (each holding a `SKILL.md`). The member adds one with the system's
   folder picker; the web never sees the path, only a token and a name (the
   local-files precedent, `local_files.rs`). A skill name found in two sources
   is the first source's; the second is listed as hidden by it.
3. **A skill is eligible only if it can be mirrored whole.** Not eligible, and
   said why in the list: a symlink that leaves the skill's folder, more than
   512 files or 8 MiB (the import limits), no `SKILL.md`, a `SKILL.md` without a
   name and description. Not copied at all: `.git`, `node_modules`, `__pycache__`,
   `.env*`, private keys and certificates, `.DS_Store`.
4. **Two independent switches per skill, both off until ticked.**
   - **In my sandbox runs** (the mirror, rule 5).
   - **Publish to my library** (rule 8): an explicit action per skill and per
     update, never a setting that syncs.
   A third, **For borrowers**, is off for every skill and ticked per skill by the
   owner: a run another member borrows from this member's drive sees the skill
   in its sandbox only then.
5. **The mirror: a read-only copy, mounted where the skill is.** The component
   keeps the ticked skills copied into a folder of its own under the workspaces
   root (`<workspaces root>/.mounts/<component>/<skill>`, a copy brought up to
   date by comparing each file's size and modification time to nanoseconds, so an
   unchanged skill costs a stat per file) and declares read-only mounts of those
   copies, one per path the skill can be reached by, at the same path in the
   guest:
   - the skill's own folder in its source;
   - every agent skills folder that holds a link to it, found by following each
     entry's link and comparing the real path, over the folders of the policy
     (rule 6) and the folders the member adds in Settings.
   An absolute path in a command, a path through any agent's link, and a path
   relative to the skill all resolve. Nothing else of the home folder is shared.
6. **Nothing is written into code: the rules are data.** The agent folders to look
   for links in, what is never copied, what looks like a credential, what counts
   as a script and every limit are one policy file shipped with the component
   (`policy.default.json`), and a member's own policy file adds to it or replaces a
   limit. Supporting another agent is a line in a file, never a change to the
   sandbox or the component.
7. **It is a component the member installs, or replaces.** Local skills are not part
   of the bridge, the sandbox or the agent's flow. They are an installable plugin
   component that speaks two small contracts (§3); a member who does not install
   it has none of this, and one who writes their own component implements the same
   contracts and gets the same Settings → Skills and the same mounts. The bridge
   knows neither skills nor this component, only the mount contract; a component
   cannot weaken the sandbox through it (§3).
8. **Publish to my library.** The desktop packs one skill (the same filters as
   rule 3), scans it (rule 9) and hands the archive to the web, which imports it
   as the member's own `skill` component through the existing import route
   (a new version of the same component when it exists). The list shows
   "published", "changed since" (the folder's hash against the one published) and
   "not published". A skill with scripts is imported with the `scripts`
   permission the importer already adds; publishing to the organization stays the
   separate approval flow.
9. **A secret scan before any file leaves the machine or enters the guest.** A
   file whose name or content looks like a credential (private key blocks, cloud
   and token prefixes, `password=`/`secret=` assignments, the patterns CI's
   secret scan already uses) stops the skill's mirror and publish with the file
   named. The member fixes it or leaves the skill out; there is no override.
10. **Changes apply on the next turn.** The space is re-ensured every turn, so a
   tick, an untick, a removed folder or an edited skill reaches the next sandboxed
   turn with no restart and none of a running turn changes.

## 3. How it fits: a component and two contracts

```
Settings → Skills (web)  ──skills-provider/v1──▶  local-skills component  ──mounts/v1 files──▶  bridge ──▶ sandbox space
   host-rendered list           (loopback HTTP)     (a desktop plugin: scans,        (a folder of      (validates, adds
   and switches                                      mirrors, packs; the policy)       JSON files)       read-only mounts)
```

- **The component** (`cmd/bnbloc-local-skills`, package `packages/local-skills`)
  is a plugin the desktop supervises as a sidecar, installed by the member like
  any optional component. It owns everything about skills (`internal/localskills`:
  scan, eligibility, filters, secret scan, mirror, reach paths, the policy). It
  declares two capabilities, `skills-provider` and `sandbox-mounts`; a plugin that
  subscribes to no room event, so a plugin that declares one of these
  capabilities needs no hook (the hook rule is waived for it).
- **Contract 1, sandbox mounts (the platform seam, component → bridge).** A
  component writes `<bridge state dir>/mounts/<component>/mounts.json`:
  `{contract:"bnbloc.sandbox.mounts/v1", mounts:[{host, guest, for}], notes:[…]}`
  (`for`: `own`, `borrowed` or `all`, default `own`). Before each sandboxed turn
  the bridge reads every such file and adds the mounts that pass, read-only, to
  the turn's space. It validates them as a component cannot be trusted to: `host`
  is under `<workspaces root>/.mounts/<component>/` (the machine's one share) and
  stays there once symlinks are resolved; `guest` is absolute, clean, not under
  the turn's workdir and not a system root; at most 256 mounts and 256 KiB per
  component, at most 16 components; every mount is read-only whatever the file
  says. A mount that fails is dropped and logged
  (`bnbloc.bridge.sandbox_mount_dropped`), never failing the turn. The contributed mounts are not part of the core's path
  mapping (only the workspace is). The bridge reads the files each turn, so an
  install, a change or an uninstall needs no restart; it knows no skill.
- **Contract 2, skills provider (the host UI ↔ a component, loopback HTTP).** The
  component serves, on its own loopback address and behind a per-launch bearer the
  desktop hands it: `GET /v1/state` (sources and their problems, every skill with
  its eligibility, reason, scripts, size, hash and the member's choice, the notes),
  `PUT /v1/settings` (enabled, sources, agent folders, the choices) and
  `POST /v1/pack` (one skill as an archive the Library imports, after the filters
  and the scan). The web never calls it directly: a Tauri command forwards to the
  plugin's own address, only for a plugin that declares `skills-provider` (three commands, one per route,
  never a generic path), and `Settings → Skills` renders what any such component returns. A member's own
  component implements the same three routes.
- **The desktop** passes a plugin that declares these capabilities its launch
  environment (a data folder for its settings, the mounts folder the bridge reads,
  the mirror root under the workspaces root, its bearer); a plugin without them
  gets none of it. Its settings (sources, ticks) live in its data folder, 0600.
- **The web** talks to the host through Tauri commands. The folder picker for a
  source is the system's and its path goes to the provider; the web does see the
  paths it lists (they are the member's own folders, shown in their own Settings).
- **Own and borrowed runs** are told apart by the bridge (`isBorrow`); it passes
  the distinction to `for` selection. A run that is not sandboxed gets no mounts.
- **The sandbox conformance suite** never tests `SpaceMount.ReadOnly`; a case that
  a read-only mount refuses writes lands first, and the bridge refuses to mount a
  contribution on a backend that has not been shown to enforce it.

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | The member installs the component and chooses skills; it runs as the member on their machine. The owner chooses skills and who may use them. A borrower's run mounts a skill only when the owner ticked "For borrowers" for it; a skill is never mounted for someone else by default. Another member never sees the owner's source folders or list; they see a published component only when the owner publishes and shares it. |
| Time and lifecycle | Applied per turn from the settings file; a running turn keeps what it started with. A removed or unticked skill's mirror is deleted at the next turn. The mirror survives a restart (it is a folder) and is rebuilt from the sources when missing. |
| Scale and cost | Per skill ≤ 512 files and 8 MiB; at most 64 ticked skills; an unchanged skill costs one stat per file per turn. The mirror is at most 512 MiB in all, then the newest ticks win and the rest are listed "over the limit". |
| Failure and recovery | A skill that cannot be copied (permission, vanished folder) is left out of the turn, named in the list, and the turn runs without it, never failing because of it. A mount the backend refuses (or a descriptor that fails validation) runs the turn without the skills; the bridge logs it, and the Settings list carries the component's own notes on what it left out. |
| Boundaries and privacy | The guest sees a read-only copy at the skill's own paths and nothing else of the home folder. Credentials never enter the guest (existing rule); a credential-looking file stops the skill (rule 9). The skills list shows the member's own folders to the member alone. |
| Abuse and untrusted input | A component is untrusted to the bridge: it can only offer read-only mounts under its own folder of the one share, and the bridge drops anything else. A local process calling the provider API needs the per-launch bearer. A skill is code the member chose to run in their own sandbox. A symlink out of the folder is not followed (eligibility); the mirror is a copy, so a skill cannot reach through it to the original. A borrower cannot tick anything. |
| One owner | Everything about skills: `internal/localskills`, behind the component. The mount seam: the bridge's validation of descriptor files. Import: `contentpkg`. |
| Defaults and fallbacks | Feature off; no sources; no skill ticked; borrowers off. Without the table's agents present, only the source path is mounted. A turn that is not sandboxed ignores all of it. |
| External contracts | Two new contracts (`bnbloc.sandbox.mounts/v1` files, `bnbloc.skills.provider/v1` loopback HTTP) and a plugin capability list (`skills-provider`, `sandbox-mounts`). The sandbox protocol is unchanged (read-only extra mounts it already has). Import is unchanged. |
| Load on users and agents | Three toggles and a list; no new tool, no prompt text, the agent sees its skills where it always did. |
| Release and compatibility | Desktop-only (macOS first: that is the sandbox backend's platform). A component nobody installs changes nothing; one installed does nothing until the member turns it on. The bridge reads mount files per turn, so no restart. Plugin validation relaxes (a hook is required unless the plugin declares `skills-provider` or `sandbox-mounts`), a change to a code-owned list, with no compatibility shim. |
| Observability | The list shows each skill's state (mirrored, not eligible and why, hidden by another source, published, changed); the bridge logs a mount it dropped. |

**Pre-mortem.**

1. **"The command still cannot find the script."** The skill was reached through a
   link the table does not name (an agent's own folder). Settings take extra
   agent folders; the mounts are every path whose real path is the skill, found
   by following links, so an unnamed agent's link is found when its folder is
   listed.
2. **"A skill's `.env` ended up in the guest."** Names and content are filtered
   and scanned before the copy, and the mirror holds only what passed; a skill
   with a credential-looking file is not mirrored at all.
3. **"My edit did not show up."** The component brings the mirror up to date every
   ten seconds (and on every settings change) by comparing each file's size and
   modification time (to nanoseconds); a file that differs is copied again.
4. **"A borrower read my private skill."** Mounts are per skill, so a borrowed turn
   gets mounts only for the skills ticked "For borrowers" and nothing else of the
   mirror is visible in its guest. (What the owner's own CLI loads on the host is
   a separate matter, §5.)
5. **"Read-only was not read-only."** The conformance case runs against every
   backend before the feature ships; until it passes the mirror is refused rather
   than mounted writable.
6. **"The list froze on a huge folder."** Scanning is bounded (depth 2, 2,000
   entries per source) and runs off the UI thread; a bigger source is cut and says so.

## 5. Not covered

- **Windows and Linux desktops**: the sandbox's guest backends there are a later
  phase; the settings and the publish work on any platform, the mirror waits.
- **Skills that call host-only tools** from the guest (a CLI that exists only on
  the host): the existing host-passthrough allowlist is the answer, not this.
- **Syncing the other direction** (the library's skills into the member's folders).

## 5b. An open decision: what a borrowed run's host CLI loads

In the hybrid execution a borrowed run is the owner's CLI on the owner's host,
and it loads the skills its own configuration names (its folders, the links in
them) whatever this feature says. "For borrowers" therefore decides only whether
the skill's *files* are in the borrowed run's guest, where its scripts run. If
the owner should also choose which skills a borrowed run may load at all, that
is a restriction on the CLI's configuration (the sandbox's confine and the
profile it is started with), a change to behaviour that exists today, and its
own decision; this PRD does not make it.

## 6. Milestones

- **A. The mount seam (Go, bridge):** read and validate descriptor files, add the
  mounts per turn (own/borrowed), the `ReadOnly` conformance case, tests.
- **B. The component:** `internal/localskills` (done: policy data, scan,
  eligibility, secret scan, mirror, reach paths, plan), `cmd/bnbloc-local-skills`
  (settings, mirror loop, descriptor writer, the provider API), the plugin package
  and its README (build, sign, install), tests.
- **C. The desktop:** plugin capabilities and the relaxed hook rule, the launch
  environment and bearer, the forwarding Tauri command, tests.
- **D. Settings → Skills:** the host-rendered list and switches over any provider.
- **E. Publish to my library:** pack through the provider, import, the
  published/changed state.
- **F. Verify** on a real sandboxed run with a skill that runs a script, through a
  `.agents` path and through an agent's link; docs.
