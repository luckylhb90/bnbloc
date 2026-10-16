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
 Settings → Skills (web, desktop only)
        │  skills_providers / _state / _settings / _pack   (Tauri invoke)
        ▼
 desktop (Rust, internal_ext/skills_provider.rs)
        │  loopback HTTP + per-launch bearer              bnbloc.skills.provider/v1
        ▼
 plugin sidecar  (cmd/bnbloc-local-skills, internal/localskills)
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

**`bnbloc.skills.provider/v1`** — what Settings → Skills renders, over loopback
with the launch's bearer:

| Call | Does |
|---|---|
| `GET /v1/state` | the member's settings as saved, the folders and their skills (name, description, size, scripts, `eligible`/`reason`, hash, the member's choice, `changed` since it was published), folders worth adding, notes on what the last publish left out |
| `PUT /v1/settings` | replaces the settings whole (unknown fields refused, bounded), brings the copies and descriptor up to date, answers the state |
| `POST /v1/pack?name=` | one skill as a tar (`X-Skill-Hash`), only if eligible, only the files the policy lets through |

The web adds nothing of its own: a skill is published to the library by packing
it through the provider and importing the archive as a skill pack
(`contentApi.importArchive`), and the publish is recorded in the settings
(`published`: hash, component, version), so a later edit shows as "Publish
changes".

## What the component decides

- **Settings** (`<data dir>/local-skills.json`, owner-only): `enabled`, `sources`
  (folders of skill folders; `~/.agents/skills` is only ever *suggested*),
  `agentDirs` added to the policy's, per-skill `{vm, borrowers, published}`, and
  an optional `policyFile`. Borrowers are only ever skills also ticked for the
  member's own sandbox.
- **Policy** (`internal/localskills/policy.default.json`, data, merged with the
  member's own file): which agent folders exist, which files a skill never
  carries (credentials, `.git`, …), size and count limits, how deep a source is
  searched. A skill that holds a credential file, a link out of its folder or
  exceeds a limit (5000 files, 256 MiB; the library's own bundle limit of 512
  files and 8 MiB only stops *publishing*, never the sandbox) is **ineligible**: not mirrored, not packed, and the Settings
  row says why. Adding an agent (Codex, opencode, a company CLI, …) is a row in
  the policy, not code.
- **Mirror**: a copy, never the member's folder. A folder is mounted only
  while its skill is ticked; an edit to the source reaches the mirror within ten
  seconds, so the next turn sees it.
- **Borrowed runs** get only what is ticked "for others borrowing my agents".

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
`_MOUNTS_FILE`, `_MOUNT_ROOT` and `_SUPERVISED` (`packages/local-skills/package.json`).

## Installing it

```sh
go build -o dist/bnbloc-local-skills-darwin-arm64 ./cmd/bnbloc-local-skills   # GOOS/GOARCH per platform
bnbloc components publish-package --package packages/local-skills/package.json \
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
