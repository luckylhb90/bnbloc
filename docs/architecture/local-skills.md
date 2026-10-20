# Local skills: an installable component

A member's agents use skills that live on their computer (`~/.agents/skills`,
`~/.claude/skills`, …). In a sandboxed run the agent's commands execute in a VM
that sees only the workspaces share, so a skill whose script lives in the
member's home is not there. The local-skills component lets the member choose,
skill by skill, what the sandbox sees and what is added to their library. **Off,
and empty, until the member turns it on.**

It is an installable plugin, not part of the app: nothing in the agent flow, the
bridge or the platform knows what it is, and a member may install their own
component that speaks the same two contracts. Design record and review:
[`../prd/2026-10-16-local-skills.md`](../prd/2026-10-16-local-skills.md).

```
 the skills panel (web, desktop only: Components → the component's card → Open)
        │  skills_providers / _state / _settings / _pack   (Tauri invoke)
        ▼
 desktop (Rust, internal_ext/skills_provider.rs)
        │  loopback HTTP + per-launch bearer              bnbloc.skills.provider/v1
        ▼
 plugin sidecar  (luckylhb90/bnbloc-local-skills)
        │  copies the ticked skills into  <workspaces root>/.mounts/<plugin>/
        │  writes  <member>/bridge/mounts/<plugin>/mounts.json   bnbloc.sandbox.mounts/v1
        ▼
 bridge: before every sandboxed turn adds the descriptor's mounts, read-only
        (docs/architecture/sandbox-mounts.md)
```

## Two contracts

**`bnbloc.sandbox.mounts/v1`** — what reaches the sandbox: a descriptor file
(`docs/architecture/sandbox-mounts.md`). The component copies each ticked skill
into its own folder under the workspaces root (the machine's one share) and
lists `{host, guest, for}`: the guest path is where the skill sits on the
member's computer (`~/.agents/skills/<name>`, and each agent folder that links
to it), so a script path an agent knows resolves in the VM. The bridge forces
read-only and validates every entry.

**`bnbloc.skills.provider/v1`** — what the skills panel renders, over loopback
with the launch's bearer. The panel opens from the card of any installed
component with the `skills-provider` capability on the Components page
("Open"; the web names no component), and once by itself right after one is
added when it needs no setup first (one place: there is no separate Settings
entry). The calls:

| Call | Does |
|---|---|
| `GET /v1/state` | the member's settings as saved, the folders and their skills (name, description, size, scripts, `eligible`/`reason`, hash, the member's choice, `changed` since it was published), folders worth adding, notes on what the last publish and the settings left out |
| `PUT /v1/settings` | replaces the settings whole (unknown fields refused, bounded), brings the copies and descriptor up to date, answers the state |
| `POST /v1/pack?name=` | one skill as a tar (`X-Skill-Hash`), only if eligible, only the files the policy lets through |

The web adds nothing of its own: a skill is published to the library by packing
it through the provider and importing the archive as a skill pack
(`contentApi.importArchive`, with the entry the member set: Publishing a skill
below), and the publish is recorded in the settings
(`published`: hash, component, version), so a later edit shows as "Publish
changes".

## What the component decides

- **Choices** (`<data dir>/local-skills.json`, owner-only, what the panel
  edits): `enabled`, `sources` (folders of skill folders; `~/.agents/skills`
  is only ever *suggested*) and per-skill `{vm, borrowers, published}`.
  Borrowers are only ever skills also ticked for the member's own sandbox.
- **Settings** (component settings declared in its `package.json`, set from
  the card's Settings like any component's, read from
  `BNBLOC_COMPONENT_SETTINGS_FILE`): the member's `extraSkillDirs` (extra
  agent skill folders under their home) and `excludes` (name patterns never
  copied), the organization's `secretNames` and `secretPatterns` (more
  credential rules) and `maxSkills`. Each list is a `lines` setting, added
  after the shipped policy's (one merge, `Policy.Merge`): a setting can add a
  rule, never remove one. A folder outside the home is left out and the panel
  says so; a pattern that does not compile is an error naming it.
- **Policy** (`localskills/policy.default.json` in the component, data, with
  the settings merged onto it): which agent folders exist, which files a skill never
  carries (credentials, `.git`, …), size and count limits, how deep a source is
  searched. A skill that holds a credential file, a link out of its folder or
  exceeds a limit (5000 files, 256 MiB; the library's own bundle limit of 512
  files and 8 MiB only stops *publishing*, never the sandbox) is **ineligible**: not mirrored, not packed, and the Settings
  row says why. A content rule's hit that is plainly a placeholder
  (`secretPlaceholders` in the policy: `not-a-real-token`, `<your key>`,
  `xxxxxx`) is not a finding; any other hit stops the skill, and the member may
  look at the file and **allow it for the sandbox** ("Allow this file": recorded
  against the file's hash, so an edited file is checked again, and never for the
  library). A file flagged by its *name* (`.env`, a key file) is never allowable. Adding an agent (Codex, opencode, a company CLI, …) is a row in
  the policy, not code.
- **Mirror**: a copy, never the member's folder. A folder is mounted only
  while its skill is ticked; an edit to the source reaches the mirror within ten
  seconds, so the next turn sees it.
- **Borrowed runs** get only what is ticked "for others borrowing my agents".

## Publishing a skill

"Add to my library" (or "Publish changes") opens a publish step: the skill is
packed by its provider, the member sees its files and sets the library entry
(name, display name, description, tags; the import route takes them beside
the archive, `?displayName=`, `?description=`, `?tag=`, and the content
validator holds them to its rules). "Review with AI" runs the `review-skill`
assist on the folder's name, its file list and SKILL.md (never another
file's content): what to fix first (a secret or a personal path left in, a
description that does not say when to use it, a step a colleague cannot
follow) and a proposed entry, applied field by field. Nothing is published
until the member presses Publish; without an assist model the step works the
same without the button.

## Launch inputs (the package names the env vars)

A plugin that declares a capability earns the matching inputs, in the env var
the package's `sidecar` block names (`core/pkgmeta`):

| Capability | The desktop sets | Package field |
|---|---|---|
| any | the plugin's private data dir | `dataDirEnv` |
| `skills-provider` | a fresh bearer every launch (kept by the desktop, never given to the web) | `tokenEnv` (required) |
| `sandbox-mounts` | the descriptor file `<member>/bridge/mounts/<plugin>/mounts.json`, and the folder for the copies `<workspaces root>/.mounts/<plugin>` | `mountsEnv`, `mountRootEnv` (required together; refused without the capability) |

Such a plugin subscribes to no hook, so the hook requirement of a plugin package
is waived when it declares one of these capabilities. Uninstalling the plugin
sweeps its descriptor (its mounts vanish from the next turn), data dir and
copies.

The component reads `BNBLOC_LOCAL_SKILLS_LISTEN_ADDR`, `_TOKEN`, `_DATA_DIR`,
`_MOUNTS_FILE`, `_MOUNT_ROOT` and `_SUPERVISED` (its `package.json`).

## Installing it

The component lives in its own repository,
[`luckylhb90/bnbloc-local-skills`](https://github.com/luckylhb90/bnbloc-local-skills)
(docs/prd/2026-10-18-components-in-own-repos.md); it builds against the public
contract package `component` (the mounts descriptor, the skill name rule and
frontmatter, the library's bundle caps). From its checkout:

```sh
go build -o dist/bnbloc-local-skills-darwin-arm64 .   # GOOS/GOARCH per platform
bnbloc components publish-package --package package.json \
  --binary darwin-arm64=dist/bnbloc-local-skills-darwin-arm64 --rollout optin
```

`optin` leaves it off until a member installs it from Components; "off" removes
it. A member's own component is published the same way with its own package file.

## Not covered

An agent CLI that runs on the host (own runs are not sandboxed by default, and a
borrowed run's CLI still loads the host's own skill folders) keeps loading the
skills it always did: this component decides what the **sandbox** sees, not what
a host CLI reads. Narrowing that for borrowed runs is an open decision in the PRD
(§5b).
