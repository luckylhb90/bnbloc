# Local skills: a member's own skills in the sandbox, and in their library (2026-10-16)

Status: proposed (2026-10-16). Builds on `docs/prd/2026-10-09-agent-vm-sandbox.md`
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
   - **Publish to my library** (rule 7): an explicit action per skill and per
     update, never a setting that syncs.
   A third, **For borrowers**, is off for every skill and ticked per skill by the
   owner: a run another member borrows from this member's drive sees the skill
   in its sandbox only then.
5. **The mirror: a read-only copy, mounted where the skill is.** Before a
   sandboxed turn the bridge brings the ticked skills into
   `<workspaces root>/.local-skills/` (a copy, brought up to date by comparing
   each file's size and modification time to nanoseconds, so an unchanged skill
   costs a stat per file) and adds read-only mounts to the turn's space, one per path the skill
   can be reached by, at the same path in the guest:
   - the skill's own folder in its source;
   - every agent skills folder that holds a link to it (the data table
     `AGENT_SKILL_DIRS`: `~/.claude/skills`, `~/.codex/skills`,
     `~/.config/opencode/skills`, `~/.gemini/skills`, `~/.cursor/skills`, and
     the folders the member adds in Settings), found by following each entry's
     link and comparing the real path.
   An absolute path in a command, a path through any agent's link, and a path
   relative to the skill all resolve. Nothing else of the home folder is
   shared.
6. **An agent is a row of data, not code.** Which agents a member has is the
   member's folders and the table above; the sandbox needs no knowledge of an
   agent beyond them. An agent the table does not name (including a company
   CLI) is added by its skills folder in Settings.
7. **Publish to my library.** The desktop packs one skill (the same filters as
   rule 3), scans it (rule 8) and hands the archive to the web, which imports it
   as the member's own `skill` component through the existing import route
   (a new version of the same component when it exists). The list shows
   "published", "changed since" (the folder's hash against the one published) and
   "not published". A skill with scripts is imported with the `scripts`
   permission the importer already adds; publishing to the organization stays the
   separate approval flow.
8. **A secret scan before any file leaves the machine or enters the guest.** A
   file whose name or content looks like a credential (private key blocks, cloud
   and token prefixes, `password=`/`secret=` assignments, the patterns CI's
   secret scan already uses) stops the skill's mirror and publish with the file
   named. The member fixes it or leaves the skill out; there is no override.
9. **Changes apply on the next turn.** The space is re-ensured every turn, so a
   tick, an untick, a removed folder or an edited skill reaches the next sandboxed
   turn with no restart and none of a running turn changes.

## 3. How it fits

- **Settings** live in the member's own directory
  (`<app config>/members/<hash>/local-skills.json`, 0600), the place the bridge
  already reads per session (MCP and plugin settings). Shape:
  `{enabled, sources:[{id, path}], agentDirs:[path], skills:{<name>:{vm, borrowers}}}`.
- **The web talks to the host through Tauri commands** (`local_skills_list`,
  `local_skills_set`, `local_skills_source_add` using the folder dialog,
  `local_skills_pack`), modelled on `local_files.rs` and `plugin_settings.rs`.
  A new section beside Agent sandbox and Tool settings, gated with
  `desktop.available()`.
- **A Go package** (`internal/localskills`: scan, eligibility, filters, secret
  scan, mirror, the path set of a skill) is the one owner; the desktop's list,
  the bridge's mirror and the publish pack all call it (the Rust side invokes it
  through the bridge binary's subcommand, as the sandbox status does).
- **Mounts** are built in `services/bridge/sandbox.go prepare()`, which already
  builds the space's mount list and re-ensures it each turn. `SpaceMount` has
  `ReadOnly`; **the sandbox conformance suite never tests it**, so a case that a
  read-only mount refuses writes lands first. The mirror sits under
  `bnbloc-workspaces/` because the machine has exactly one share, fixed at its
  creation.
- **Own and borrowed runs** are told apart by `d.borrowed` (`isBorrow`). A run
  that is not sandboxed gets no mirror (it reads the real folders).

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | The owner chooses skills and who may use them. A borrower's run mounts a skill only when the owner ticked "For borrowers" for it; a skill is never mounted for someone else by default. Another member never sees the owner's source folders or list; they see a published component only when the owner publishes and shares it. |
| Time and lifecycle | Applied per turn from the settings file; a running turn keeps what it started with. A removed or unticked skill's mirror is deleted at the next turn. The mirror survives a restart (it is a folder) and is rebuilt from the sources when missing. |
| Scale and cost | Per skill ≤ 512 files and 8 MiB; at most 64 ticked skills; an unchanged skill costs one stat per file per turn. The mirror is at most 512 MiB in all, then the newest ticks win and the rest are listed "over the limit". |
| Failure and recovery | A skill that cannot be copied (permission, vanished folder) is left out of the turn, named in the list, and the turn runs without it, never failing because of it. A mount the backend refuses runs the turn without the skills and says so in the turn's notice. |
| Boundaries and privacy | The guest sees a read-only copy at the skill's own paths and nothing else of the home folder. Credentials never enter the guest (existing rule); a credential-looking file stops the skill (rule 8). The web never sees a path. |
| Abuse and untrusted input | A skill is code the member chose to run in their own sandbox. A symlink out of the folder is not followed (eligibility); the mirror is a copy, so a skill cannot reach through it to the original. A borrower cannot tick anything. |
| One owner | The scan, filters, eligibility, secret scan, mirror and path set: `internal/localskills`. Mounts: `sandbox.go prepare()`. Import: `contentpkg`. Settings file: the member directory convention. |
| Defaults and fallbacks | Feature off; no sources; no skill ticked; borrowers off. Without the table's agents present, only the source path is mounted. A turn that is not sandboxed ignores all of it. |
| External contracts | A new settings file and four Tauri commands, internal to the desktop. The sandbox protocol is unchanged (read-only extra mounts it already has). Import is unchanged. |
| Load on users and agents | Three toggles and a list; no new tool, no prompt text, the agent sees its skills where it always did. |
| Release and compatibility | Desktop-only (macOS first: that is the sandbox backend's platform). Off until the member turns it on, so an upgrade changes nothing for anyone. The bridge reads the settings per session, so no restart. |
| Observability | The list shows each skill's state (mirrored, not eligible and why, hidden by another source, published, changed); a turn's notice says when skills were left out. |

**Pre-mortem.**

1. **"The command still cannot find the script."** The skill was reached through a
   link the table does not name (an agent's own folder). Settings take extra
   agent folders; the mounts are every path whose real path is the skill, found
   by following links, so an unnamed agent's link is found when its folder is
   listed.
2. **"A skill's `.env` ended up in the guest."** Names and content are filtered
   and scanned before the copy, and the mirror holds only what passed; a skill
   with a credential-looking file is not mirrored at all.
3. **"My edit did not show up."** The mirror is brought up to date at the start of
   every sandboxed turn by comparing each file's size and modification time (to
   nanoseconds); a file that differs is copied again.
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

- **S1:** `internal/localskills` (scan, eligibility, filters, the path set, the
  secret scan) with tests; a bridge subcommand listing a source.
- **S2:** the mirror, its mounts in `prepare()`, the own/borrowed rule, the
  `ReadOnly` conformance case, tests; settings read per session.
- **S3:** Settings → Skills (Tauri commands, folder picker, the list and its
  states, the three switches).
- **S4:** publish to my library (pack, scan, import, published/changed state).
- **S5:** verify on a real sandboxed run with a skill that runs a script, through
  a `.agents` path and through an agent's link; docs.
