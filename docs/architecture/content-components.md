# Content components: experts, skill packs and prompt packs

Design: [`docs/prd/2026-10-12-experts-and-teams.md`](../prd/2026-10-12-experts-and-teams.md)
(delivery steps 1 and 3). This is the durable reference for how content components
are stored, who may use them, how a turn wears them, and the HTTP surface.

## What they are

Content components are packages that are mostly text, authored by members
(and later by the platform), beside the executable kinds (cores, plugins,
sandboxes, previewers, notifiers, auth providers):

| Kind | Manifest kind | Files |
|---|---|---|
| Expert | `bnbloc.expert.package/v1` | `component.json`, `INSTRUCTIONS.md`, optional `README.md`, embedded skills `skills/<name>/SKILL.md` (+ scripts/, references/) |
| Skill pack | `bnbloc.skill.package/v1` | `component.json` (`skills: [names]`), `<name>/SKILL.md` per skill, optional `README.md` |
| Prompt pack | `bnbloc.prompt.package/v1` | `component.json` (its `prompts`), optional `README.md` (see "Prompt packs") |

A package is a plain ustar bundle (`internal/tarbundle`, limit profile
`tarbundle.ContentLimits`: 8 MiB, 512 files; a prompt pack's own profile,
`contentpkg.PromptLimits`, is 1 MiB and 2 files). **One validator,
`internal/contentpkg`**, backs every entry point (form saves, archive
uploads, imports, the bridge's materialization):

- the shared envelope in `component.json` (strict decoding): `kind`,
  `name` (`[a-z0-9-]`, ≤ 63), semver `version`, `displayName`,
  `description`, `category`, `tags`, `icon`, `requires` (`skills` /
  `prompts` as `name@range`, `mcp` by tool name with env names,
  `permissions`), plus the kind's fields (`quickPrompts`, `welcome`;
  `skills`; `prompts`). A `team` block is refused until expert teams exist;
- declared permissions: `scripts`, `mcp.stdio`, `mcp.http:<host>`. A
  package that ships scripts (a `scripts/` or `bin/` dir, or a script
  extension) must declare `scripts`. stdio MCP servers come only from MCP
  tool components referenced by name; none exists yet, so every
  `requires.mcp` is refused;
- `SKILL.md` frontmatter must carry `name` (= the skill directory) and a
  `description` (the agent loads the skill from it).

The kind registry (`contentpkg.KindSpec`: short name, limit profile, the
kind's checks, its form layout, its import of foreign formats, and
`MemberUse` — whether a member turns it on for themselves) is how a new
kind joins without branching the shared code.

### Import

`contentpkg.Import` turns a tar, tar.gz or zip of an existing format into a
package with a generated manifest: a bnbloc package (re-validated, version
rewritten), a skill directory (`SKILL.md` at the root), a skills folder
(`<name>/SKILL.md` or `skills/<name>/SKILL.md`), or a Claude Code-style
plugin (`.claude-plugin/plugin.json`: its `skills/` become the skills; for an
expert its first `agents/<name>.md` body becomes the instructions), or, for
a prompt pack, markdown and json files of prompts (below). A single text
file uploads without an archive. Each kind reads its foreign formats in its
`KindSpec` import hook. It drops OS/VCS junk, refuses links and unsafe file
names, declares `scripts` when a skill ships scripts, and stops an archive
bomb at the cap.

## Prompt packs

A curated, versioned set of prompts with variables, used from the composer
(PRD §4). The prompts live in `component.json`, so a view reads them from
the stored manifest without fetching the bundle:

```
prompts: [{id, title, body, variables: [{name, label?, default?, options?}], tags?, slash?: "/name"}]
```

- **Validator.** At least one and at most 100 prompts; ids (`[a-z0-9-]`)
  and slash commands (`/` + `[a-z0-9-]`) unique in the pack; a title (one
  line, ≤ 120) and a body (≤ 8000 characters); every `{{name}}` in a body
  (spaces inside the braces allowed; anything else between braces is
  literal text) is a declared variable and every declared variable is
  used; options unique, a default one of them; tags as in the envelope.
  Experts and skill packs refuse `prompts`.
- **Import.** A markdown file holds one prompt per `##` heading (per `#`
  heading when it has no `##`; a top `#` then names the pack and the text
  under it describes it; `id:`, `slash:` and `tags:` lines right under a
  heading set those fields; headings in code fences do not split); a json
  file is a list of prompts or `{name?, displayName?, description?,
  prompts}` (`prompt`/`content`/`text` and `command` are read as `body` and
  `slash`). Placeholders are declared as variables (labels from their
  names), unused declarations dropped, ids made from titles and made
  unique (`NormalizePrompts`).
- **Uses.** Like every content kind: a member turns a pack on for
  themselves (member use) and a room's owner or admins turn it on for the
  room (room use). The composer offers the prompts of the member's packs,
  then the room's (`GET /v1/content-uses?room=`); its picker searches them,
  a message that starts with `/word` lists matching slash commands, and a
  prompt with variables asks for them in a small form before its text is
  inserted.
- **The personal pack.** "Save as prompt" on a message
  (`POST /v1/content-components/prompts`) appends the prompt to the
  member's own pack `my-prompts` ("My prompts") as a new version (patch
  bump), creating it, and turning it on for them, on the first save. It is
  a normal pack: viewable, editable and deletable in the Library.
- **Usage counts.** `content_prompt_uses (component_id, prompt_id,
  use_count, last_used_at_ms)` counts insertions, one upsert per insertion
  (`POST /v1/content-components/{id}/prompts/{promptId}/use`, readable packs
  only), never per keystroke; views carry each prompt's `uses`. Deleting the
  component deletes its counts.
- **Expert quick prompts** may point at a pack's prompt (`{title?, ref:
  "pack/prompt-id"}`, the pack named in `requires.prompts`). Views resolve
  the reference for the viewer, and in the room for room uses and `?room=`
  reads, to the highest version satisfying the range among the packs they
  may use and the room's (`contentuse.ResolvePromptRef`); an unresolved
  one says which pack it needs.

## Built-in content

Some content ships with the release: today the **Expert builder**
(`cc_builtin_expert-builder`), which authors experts and skill packs by
conversation (PRD §5). It is a content source, not a second library:

- **Source.** `internal/contentbuiltin` embeds package directories
  (`packages/<name>/`, the directory is the package name), packs each with
  `tarbundle.PackFiles` and validates it with `contentpkg` when the process
  starts; an invalid built-in refuses to start (its test fails first). It is
  versioned with the release: changing a built-in's files means bumping its
  `component.json` version, so a pinned job or room use never sees other
  bytes under one version.
- **Overlay.** `contentbuiltin.Overlay(store)` wraps the content store the
  gateway and realtime use (their `cmd` mains): reads of a `cc_builtin_<name>`
  id answer from the binary, listings include built-ins when the query asks
  for scope `builtin` or names their ids (built-ins have no tenant and no
  owner), writes are refused (`ErrReadOnly`). Uses and acks of a built-in are
  ordinary rows (a room may pin the builder; `PutContentUse` stores a
  `cc_builtin_` id without a component row, the overlay vouches for it).
- **Visibility.** Scope `builtin` in `extpipe.MemberVisibility`: visible
  and usable by every signed-in member, owned by nobody (read-only in the
  Library). The chat path (`contentuse.Usable`), `contentuse.Effective` and
  the bundle entitlement treat it like any other component.
- **Bundle.** Its artifact key is `builtin/<sha256>`; `GET /v1/artifacts/{key}`
  serves it from the binary after the usual entitlement, and the gateway's
  own views read it the same way. The blob store and its collection never
  hold it.

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
  `member` (a skill pack for all of a member's agents, a prompt pack in
  their composer) or `room` (room skill packs and prompt packs, pinned
  experts) — at a pinned version or following the latest (`''`).
- `content_permission_acks`: a member confirmed a version's declared
  permissions (first use).
- `content_prompt_uses`: per-prompt insertion counts of prompt packs
  (migration `2026_10_prompt_packs`, which also dropped the former prompt
  library's `prompts` table).

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
| `GET /v1/content-components?kind=expert\|skill\|prompt` | The caller's Library: their own components plus org/platform ones visible to them, at the latest version |
| `POST /v1/content-components` | Save a new component: `{form, version?}` (JSON) or a package archive (`component.json` names it). 201 detail; 409 duplicate name; 400 validator message. With `?job=&room=`: a save by the bridge running the job, as its requester (below) |
| `POST /v1/content-components/import?kind=&name=` | Import an archive (tar, tar.gz, zip) or a single markdown/json file of prompts as a new component; `?id=` imports a new version of the caller's component |
| `POST /v1/content-components/prompts` | "Save as prompt": `{title?, body, tags?}` into the caller's personal prompt pack (created on the first save); 201 `{component, promptId, created}` |
| `POST /v1/content-components/{id}/prompts/{promptId}/use[?room=&version=]` | Count one insertion of a pack's prompt (204) |
| `GET /v1/content-components/{id}[?room=]` | Detail with versions (readable when visible, or used by a room of the caller's) |
| `DELETE /v1/content-components/{id}` | Delete (owner) |
| `POST /v1/content-components/{id}/versions` | New immutable version: `{form, baseVersion, version?}` (the base's extra files carry over) or an archive; owner only; strictly newer |
| `GET /v1/content-components/{id}/versions/{v}[?room=]` | The version: component as of it, instructions, skill bodies, files (text when viewable) |
| `POST /v1/content-components/{id}/versions/{v}/ack[?room=]` | Confirm the version's declared permissions |
| `GET /v1/content-uses[?room=]` | The caller's member uses and a room's uses, each with its component at the version in effect and `updateAvailable` |
| `PUT /v1/content-uses` | `{scope: member\|room, roomId?, componentId, version?}`: member scope takes the kinds with `MemberUse` (skill and prompt packs); room scope needs the room's owner or an admin |
| `DELETE /v1/content-uses?scope=&roomId=&componentId=` | Remove a use |
| `GET /v1/content-components/effective?job=&room=` | The bridge running a job: what its turn wears |
| `GET /v1/artifacts/{key}[?room=\|?job=&room=]` | Bundle download (`artifactEntitled`) |

**Requests on behalf of a job's requester.** The room tools
(`save_component`, `get_component`, `list_components`; see
[`room-tools.md`](room-tools.md)) call `POST /v1/content-components`,
`POST …/{id}/versions`, `GET …/{id}`, `GET …/{id}/versions/{v}` and
`GET /v1/content-components` with a `?job=&room=` grant
(`services/gateway/content_requester.go`). The grant follows dispatch's
authority model: it is admitted only while the job is live and leased by a
bridge of the caller (`store.JobLeasedBy`) and the job's requester is still a
human member of the room; the request then acts as the requester (their
Library, personal scope, owner = requester). When the agent is not the
requester's own (borrowed or platform), a package that ships scripts is
refused (403): someone else's machine may not plant executable content
into a member's own Library, where own content runs unsandboxed. Every
granted save is an `extension_audit` row (kind `content`, action `save`,
actor = requester, detail naming the agent and the bridge). No other route
honors the grant.

**Saving deliverables.** Without room tools the drafted files are ordinary
deliverables: the web offers "Save as expert" / "Save as skill pack" under a
turn's files when they include a `component.json` of a content kind, packs
the room's latest files under that directory as a tar and calls
`POST /v1/content-components` (or `…/{id}/versions` for one of the member's
own) as the clicking member; the validator's message shows inline.

`GET /v1/components` also lists the member's content components through the
same visibility function, in a separate `contentComponents` array (the
desktop's executable catalog decodes `components` strictly; nothing of a
content component runs as a supervised process).
