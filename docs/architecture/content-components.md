# Content components: experts and skill packs

Design: [`docs/prd/2026-10-12-experts-and-teams.md`](../prd/2026-10-12-experts-and-teams.md)
(delivery step 1). This is the durable reference for how content components
are stored, who may use them, how a turn wears them, and the HTTP surface.

## What they are

Content components are packages that are mostly text, authored by members
(and later by the platform), beside the executable kinds (cores, plugins,
sandboxes, previewers, notifiers, auth providers):

| Kind | Manifest kind | Files |
|---|---|---|
| Expert | `bnbloc.expert.package/v1` | `component.json`, `INSTRUCTIONS.md`, optional `README.md`, embedded skills `skills/<name>/SKILL.md` (+ scripts/, references/) |
| Skill pack | `bnbloc.skill.package/v1` | `component.json` (`skills: [names]`), `<name>/SKILL.md` per skill, optional `README.md` |

Prompt packs (`bnbloc.prompt.package/v1`) register a third kind in step 3.

A package is a plain ustar bundle (`internal/tarbundle`, limit profile
`tarbundle.ContentLimits`: 8 MiB, 512 files). **One validator,
`internal/contentpkg`**, backs every entry point (form saves, archive
uploads, imports, the bridge's materialization):

- the shared envelope in `component.json` (strict decoding): `kind`,
  `name` (`[a-z0-9-]`, ≤ 63), semver `version`, `displayName`,
  `description`, `category`, `tags`, `icon`, `requires` (`skills` /
  `prompts` as `name@range`, `mcp` by tool name with env names,
  `permissions`), plus the kind's fields (`quickPrompts`, `welcome`;
  `skills`). A `team` block is refused until expert teams exist;
- declared permissions: `scripts`, `mcp.stdio`, `mcp.http:<host>`. A
  package that ships scripts (a `scripts/` or `bin/` dir, or a script
  extension) must declare `scripts`. stdio MCP servers come only from MCP
  tool components referenced by name; none exists yet, so every
  `requires.mcp` is refused;
- `SKILL.md` frontmatter must carry `name` (= the skill directory) and a
  `description` (the agent loads the skill from it).

The kind registry (`contentpkg.KindSpec`: short name, limit profile, the
kind's checks and its form layout) is how a new kind joins without
branching the shared code.

### Import

`contentpkg.Import` turns a tar, tar.gz or zip of an existing format into a
package with a generated manifest: a bnbloc package (re-validated, version
rewritten), a skill directory (`SKILL.md` at the root), a skills folder
(`<name>/SKILL.md` or `skills/<name>/SKILL.md`), or a Claude Code-style
plugin (`.claude-plugin/plugin.json`: its `skills/` become the skills; for an
expert its first `agents/<name>.md` body becomes the instructions). It drops
OS/VCS junk, refuses links and unsafe file names, declares `scripts` when a
skill ships scripts, and stops an archive bomb at the cap.

## Storage

Tables (migration `2026_10_content_components`):

- `content_components`: id (`cc_…`), kind, name, tenant, owner, `scope`
  (`personal` | `shared` | `org` | `platform`), `status` (`active` |
  `pending` | `retired`), `latest_version` and the latest manifest's display
  fields; `(tenant, scope, owner, kind, name)` is unique.
- `content_component_versions`: immutable versions with `manifest_json`,
  `bundle_blob_key` + `sha256` (+ `signature` for platform-signed
  versions). A new version is appended only while `latest_version` is
  still the one it was based on, and must be semver-newer.
- `content_component_uses`: where a component is enabled or pinned —
  `member` (a skill pack for all of a member's agents) or `room` (room
  skill packs and pinned experts) — at a pinned version or following the
  latest (`''`).
- `content_permission_acks`: a member confirmed a version's declared
  permissions (first use).

Bundles live in realtime's content-addressed blob store, like sidecar and
previewer artifacts; `RetentionStore.ReferencedBlobKeys` includes
`content_component_versions.bundle_blob_key`, so blob collection never
deletes a version's bundle. Deleting a component deletes its rows; the
collection reclaims the bundle. Content components are user content (like
room templates): no retention purge applies to them.

Step 1 creates personal scope only; the scope and status columns carry the
shared, org and platform scopes of step 4.

## Who may use what

`extpipe.MemberVisibility(viewer, gate, installedAt)` is the one answer to
"may member M use component C", for executable records
(`GateForRecord`: platform scope, rollout + tenants + opt-in) and content
(`GateForContent`): personal and shared content is its owner's in its
tenant, org content its tenant's, platform content takes the rollout gate;
retired content is hidden, pending content is its author's alone.

`internal/contentuse` adds the room: a component pinned or enabled in a
room is usable by that room's turns. `contentuse.Usable` (the chat path)
and `contentuse.Effective` (a job's turn) both decide through it.

## How a turn wears it

1. **Chat.** A chat to an agent may carry `expert: {id, version?}`.
   Realtime resolves it as the author (`contentuse.Usable`; 400 `expert not
   available` otherwise) and pins the version on the job: the requested
   one, else the room's pinned version, else the latest. The message
   payload carries `expert`, the job `expert_id/version/name`, the run
   record `expert`, and `extension_audit` gets a `content`/`use` row
   ("expert Reviewer v1.1.0 on agent jack/acme/codex (job …, room …)").
2. **Resolution.** The bridge running the job asks
   `GET /v1/content-components/effective?job=&room=` (authorized by
   `store.JobLeasedBy`: the job is live and leased by the caller's
   bridge). `contentuse.Effective` returns the job's expert at its pinned
   version, the skill packs the expert requires (highest version satisfying
   `name@range` among what the requester may use and the room's packs),
   the room's skill packs and the agent owner's own skill packs — each
   once — plus unmet requirements.
3. **Download.** Each bundle is fetched from `GET /v1/artifacts/{key}?job=&room=`
   (`artifactEntitled` admits a bundle in effect for a job the caller's
   bridge runs), verified against its sha256 and validated again.
4. **Profile.** A core advertising `agentProfile` receives the profile
   (instructions + skills + the session contributors' MCP servers) on
   session create and maps it natively (see
   [`core-providers.md`](core-providers.md)). Any other core gets the
   fallback: the instructions and a skill summary prefixed to the session's
   first prompt, the skill files under `.agents/skills/` when it accepts
   workspace files, the bodies inlined otherwise.
5. **Sessions.** The profile's content checksum (instructions + skill
   files; MCP servers and env excluded) is part of the session binding: a
   turn wearing different content starts a new session; a reused sandbox
   workdir has its agent-config paths cleared first.

## Safety

- Declared permissions show on cards and are confirmed on first use
  (`POST …/versions/{v}/ack`; the web asks before a member first sends with
  someone else's expert).
- Content that is not the agent owner's own and declares `scripts` or
  `mcp.stdio` forces a sandboxed drive (`decideSandbox`), and is refused with
  a `sandbox-required` notice where the sandbox cannot take it.
- The agent-config paths (`CLAUDE.md`, `AGENTS.md`, `.claude/skills/**`,
  `.agents/skills/**`, `.opencode/**`, `.mcp.json`) are bnbloc-owned: never
  published as deliverables or site content, never code-synced, and
  write-denied in sandboxed runs (`hostConfine.denyWrite`).
- Secrets never ship in a package: requirements name env variables.

## HTTP surface (gateway)

The gateway serves these routes itself (it owns the component surface, the
store and the artifact entitlement). JSON shapes: `internal/wire/content_components.go`.

| Route | What |
|---|---|
| `GET /v1/content-components?kind=expert\|skill` | The caller's Library: their own components plus org/platform ones visible to them, at the latest version |
| `POST /v1/content-components` | Save a new component: `{form, version?}` (JSON) or a package archive (`component.json` names it). 201 detail; 409 duplicate name; 400 validator message |
| `POST /v1/content-components/import?kind=&name=` | Import an archive (tar, tar.gz, zip) as a new component; `?id=` imports a new version of the caller's component |
| `GET /v1/content-components/{id}[?room=]` | Detail with versions (readable when visible, or used by a room of the caller's) |
| `DELETE /v1/content-components/{id}` | Delete (owner) |
| `POST /v1/content-components/{id}/versions` | New immutable version: `{form, baseVersion, version?}` (the base's extra files carry over) or an archive; owner only; strictly newer |
| `GET /v1/content-components/{id}/versions/{v}[?room=]` | The version: component as of it, instructions, skill bodies, files (text when viewable) |
| `POST /v1/content-components/{id}/versions/{v}/ack[?room=]` | Confirm the version's declared permissions |
| `GET /v1/content-uses[?room=]` | The caller's member uses and a room's uses, each with its component at the version in effect and `updateAvailable` |
| `PUT /v1/content-uses` | `{scope: member\|room, roomId?, componentId, version?}`: member scope takes skill packs; room scope needs the room's owner or an admin |
| `DELETE /v1/content-uses?scope=&roomId=&componentId=` | Remove a use |
| `GET /v1/content-components/effective?job=&room=` | The bridge running a job: what its turn wears |
| `GET /v1/artifacts/{key}[?room=\|?job=&room=]` | Bundle download (`artifactEntitled`) |

`GET /v1/components` also lists the member's content components through the
same visibility function, in a separate `contentComponents` array (the
desktop's executable catalog decodes `components` strictly; nothing of a
content component runs as a supervised process).
