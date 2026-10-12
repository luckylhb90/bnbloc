# Content components: experts, skill packs and prompt packs

Design: [`docs/prd/2026-10-12-experts-and-teams.md`](../prd/2026-10-12-experts-and-teams.md)
(delivery steps 1, 3 and 4, and the "Gap closure"). This is the durable reference for how content components
are stored, who may use them, how they are shared and published, how a turn
wears them, and the HTTP surface.

## What they are

Content components are packages that are mostly text, authored by members
(and later by the platform), beside the executable kinds (cores, plugins,
sandboxes, previewers, notifiers, auth providers, MCP tools — the tool
servers content requires, see "MCP tool components"):

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
  `skills`; `prompts`). An expert may carry a `team` block (see "Expert
  teams"); every other kind refuses one;
- declared permissions: `scripts`, `mcp.stdio`, `mcp.http:<host>`. A
  package that ships scripts (a `scripts/` or `bin/` dir, or a script
  extension) must declare `scripts`. MCP servers come only from MCP tool
  components: `requires.mcp: [{name, env?}]` names one, and a save checks
  that it is an MCP tool component the author may use, that `env` names
  only its variables, and that the package declares its transport's
  permission (`mcp.stdio`, or `mcp.http:<host>` of its URL). Reading stored
  content checks the syntax only, so a tool uninstalled later never makes
  content invalid (use time reports it missing). The Library form edits
  `requires.mcp`: the author picks from the MCP tools visible to them,
  ticks the optional env names the content needs (required ones are always
  needed), and the form derives the matching `mcp.stdio` /
  `mcp.http:<host>` permissions from the picked tools (other declared
  permissions are kept); the save check stays the authority. The expert
  form also edits a quick prompt's `ref`: picking a prompt of a pack the
  author can see adds the pack to `requires.prompts` (`name@^major`, an
  existing range kept);
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
prompts: [{id, title, body, variables: [{name, label?, default?, options?}], tags?, slash?: "/name", starter?: true}]
```

- **Validator.** At least one and at most 100 prompts; ids (`[a-z0-9-]`)
  and slash commands (`/` + `[a-z0-9-]`) unique in the pack; a title (one
  line, ≤ 120) and a body (≤ 8000 characters); every `{{name}}` in a body
  (spaces inside the braces allowed; anything else between braces is
  literal text) is a declared variable and every declared variable is
  used; options unique, a default one of them; tags as in the envelope;
  `starter` is a flag. Experts and skill packs refuse `prompts`.
- **Import.** A markdown file holds one prompt per `##` heading (per `#`
  heading when it has no `##`; a top `#` then names the pack and the text
  under it describes it; `id:`, `slash:` and `tags:` lines right under a
  heading set those fields, and `starter: yes` flags a starter; headings
  in code fences do not split); a json
  file is a list of prompts or `{name?, displayName?, description?,
  prompts}` (`prompt`/`content`/`text` and `command` are read as `body` and
  `slash`; `starter` is read as is). Placeholders are declared as variables (labels from their
  names), unused declarations dropped, ids made from titles and made
  unique (`NormalizePrompts`).
- **Uses.** Like every content kind: a member turns a pack on for
  themselves (member use) and a room's owner or admins turn it on for the
  room (room use). The composer offers the prompts of the member's packs,
  then the room's (`GET /v1/content-uses?room=`); its picker searches them,
  a message that starts with `/word` lists matching slash commands, and a
  prompt with variables asks for them in a small form before its text is
  inserted.
- **Starter prompts.** An empty room's starter prompts (above the composer)
  lead with the prompts flagged `starter` in the packs turned on for the
  room (at most 6, each pack once, in the room's order), then the generic
  examples. The pack's author flags them; the room's owner chooses by
  turning the pack on, so a room keeps no second list of its own. A member's
  own packs add no starters.
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
(`cc_builtin_expert-builder`, 1.1.0), which authors experts, skill packs and
prompt packs by conversation (PRD §5): the Library's Experts, Skills and
Prompts tabs open the workshop with "Create with AI" and "Improve with AI"
(`views/library/workshopFlow.ts`), and its instructions carry one example
package per kind, validated by the builder's test. It is a content source, not a second library:

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
  `pending` | `retired`), `latest_version`, the latest manifest's display
  fields, `source_id` (the author's component a shared copy or an org
  publication was made from) and `shared_by` (a copy's author; its owner is
  the recipient); `(tenant, scope, owner, kind, name, source_id)` is unique
  (migration `2026_10_content_sharing`). `enabled_tenants` limits platform
  content to some organizations (JSON, NULL = every one; migration
  `2026_10_content_platform_tenants`). `org_name` is an organization row's
  name (NULL for every other scope) under `UNIQUE (tenant_id, kind,
  org_name)`: an organization has one component of a kind and name
  (migration `2026_10_content_org_names`, which kept the oldest of any
  duplicates and unpublished the others; restoring one claims the name or
  fails with `ErrContentOrgNameTaken`). An unpublished org row keeps its
  name; removing it frees the name.
- `content_component_versions`: immutable versions with `manifest_json`,
  `bundle_blob_key` + `sha256` (+ `signature` for platform-signed
  versions). A new version is appended only while `latest_version` is
  still the one it was based on, and must be semver-newer.
- `content_component_uses`: where a component is enabled or pinned —
  `member` (a skill pack for all of a member's agents, a prompt pack in
  their composer), `room` (room skill packs and prompt packs, pinned
  experts) or `room-expert` (the room's expert, one of its pinned experts;
  scope id = the room, at most one) — at a pinned version or following the
  latest (`''`). A `room-expert` row is not counted as a second use.
- Usage counts read `agent_jobs` on `idx_agent_jobs_expert (tenant_id,
  expert_id, requester_user_id)` (migration
  `2026_10_agent_jobs_expert_usage`): an expert's runs and distinct
  requesters; a pack's members are its `member` uses. Each count stops at
  `store.MaxUsageCount` (a card's count is a badge, bounded like unread).
- `content_permission_acks`: a member confirmed a version's declared
  permissions (first use).
- `content_prompt_uses`: per-prompt insertion counts of prompt packs
  (migration `2026_10_prompt_packs`, which also dropped the former prompt
  library's `prompts` table).
- `content_room_shares (room_id, component_id)`: an author's standing share
  with a room (tenant, version, author, note), moved forward (never back)
  by a later share, deleted by its revocation or with the component
  (migration `2026_10_content_room_shares`).
- `content_publish_requests`: requests to publish a version of an author's
  component to the organization (`pending` | `approved` | `declined` |
  `withdrawn`), with the note, the decision, its note and who took it;
  `pending_key` (the component id while pending) admits one pending request
  per component. Deleted with the component.
- `content_drafts`: unsaved experts and skill packs staged for one quick
  try (`cd_…`; the room tools' `try_draft`, "Quick tries of drafts" in
  [room-tools.md](room-tools.md)): tenant, room, the staging job,
  requester, author (the staging agent's owner), kind, name, version,
  manifest, bundle key, `expires_at_ms` (one hour; migration
  `2026_10_content_drafts`). Not a component: nothing lists it. Each
  retention pass deletes expired rows (`idx_content_drafts_expires`).
- `agent_jobs.room_expert_id/version/name`: the room expert a job's turn
  wore (migration `2026_10_agent_jobs_room_expert`), below.

Bundles live in realtime's content-addressed blob store, like sidecar and
previewer artifacts; `RetentionStore.ReferencedBlobKeys` includes
`content_component_versions.bundle_blob_key` and
`content_drafts.bundle_blob_key`, so blob collection never deletes a
version's bundle (or a draft's while its row exists). Deleting a component deletes its rows; the
collection reclaims the bundle. Content components and their publish
requests are user content (like room templates): no retention purge applies
to them. A shared copy, an org publication and a platform row reference the
same content-addressed bundles as the versions they were made from (version
rows are cloned, bytes never), so blob collection keeps a bundle while any
of them holds it.

## Who may use what

`extpipe.MemberVisibility(viewer, gate, installedAt)` is the one answer to
"may member M use component C", for executable records
(`GateForRecord`: platform scope, rollout + tenants + opt-in) and content
(`GateForContent`): personal content and a shared copy are their owner's
(a copy's recipient) in its tenant, org content its tenant's, platform
content every tenant's it allows (`enabled_tenants`, the component tenant
scope executable records carry, passed as the gate's `Tenants`); org and
platform content take rollout auto (an
approval or a signed install is the publication, and members opt in per
use: an expert per message, a pack turned on; there is no second opt-in);
retired (unpublished) content is hidden, pending content is its author's
alone. `contentuse.Visible` is the one listing of what a member can see
(their own and the copies they hold, their organization's, the platform's,
the built-in ones): the Library, `name@range` resolution (skill and prompt
requirements, team roles) and prompt search all read it.

`internal/contentuse` adds the room: a component pinned or enabled in a
room is usable by that room's turns. `contentuse.Usable` (the chat path)
and `contentuse.Effective` (a job's turn) both decide through it.

## Sharing and publishing

Design: PRD §1 "Sources, scopes and trust" and "Status: step 4".

- **Shared copies.** The author of a personal component shares a version
  (`POST …/{id}/shares {members?, roomId?, version?, note?}`) with named
  members of their organization or with a room (its human members at that
  moment; the author must be one), at most 100 at a time. The copy rule
  lives in `internal/contentshare` (`Copy`), shared by the gateway's share
  route and realtime's member-join step. Each recipient
  gets a component of scope `shared` they own, with `source_id` = the
  author's component and `shared_by` = the author, whose versions are the
  author's version rows cloned onto the same bundles. A recipient's
  existing copy moves forward to a newer shared version, never back. A copy
  is read-only (no new versions, no re-sharing); its holder deletes it or
  updates it to the author's latest (`POST …/{id}/update`), which appends
  the author's current version while the author still has the component.
  The author's later edits and deletion do not change it. Why a copy and
  not a grant: every read path already handles an owned row, and a grant
  would die with the author's component.
- **Room shares stand.** A share with a room is recorded
  (`content_room_shares`; the reply's `room`); a member who joins the room
  later gets a copy too. Every human join in bnbloc-room (add member, join a
  shared room, redeem an invite, a template's default members; never a
  re-role) publishes a transient `member_joined` room event (`{userId}`);
  realtime's internal publish tap (`services/realtime/content_shares.go`)
  hands the joiner a copy of each standing room share
  (`contentshare.GrantRoomShares`: never the author's own, never a share
  whose author is no longer a human member of the room, never a source that
  is no longer its author's personal component), with the `content_shared`
  notification (one per version per member) and a `share` audit row naming
  the room. `GET …/{id}/shares` lists the room shares beside the copies;
  the author revokes one with `DELETE …/{id}/shares/rooms/{room}` (audit
  `unshare`): later joiners get nothing, existing copies stay.
- **Trust follows the author.** `ContentComponent.Author()` (the copy's
  `shared_by`, else the owner) is the owner `contentuse.Ref` reports to the
  bridge, so someone else's script-bearing or stdio-MCP content forces the
  sandbox on the holder's own agent too, and a copy's permissions are
  confirmed on first use (`acked` only for content the caller wrote).
- **Organization scope** is a second row, approved version by version.
  `POST …/{id}/publish {scope: "org", version?, note?}` (the author; the
  room tools' `publish_component` with a job grant) opens a publish request
  for a version (default the latest); a newer request withdraws a pending
  one, and the organization's current version cannot be requested again.
  Org admins (`manage_org` in that organization; a super admin acting
  in it) list requests, review one (the version's declared
  permissions, the files that execute (`Package.ScriptFiles`),
  requirements including MCP tools, a team's roster, skills, and the
  file-by-file changes from the version the organization uses, text
  bounded like a version detail) and approve or decline with a note. An
  approval claims the decision, then creates or moves forward the org row
  (scope `org`, owner = the author, `source_id` = the author's component)
  to that version and makes it active; if that fails the request is
  reopened. Members use the org row's latest version, so they stay on the
  approved one until a newer request is approved. **An organization has one
  component of a kind and name** (`org_name`): `name@range` stays the
  reference syntax everywhere and is never ambiguous between two authors'
  org components. A request, or an approval, of a name another author's org
  row holds (published or unpublished) is a 409 that says who holds it and
  to rename (the author saves it under a new name); the store refuses the
  clash as well (`ErrContentOrgNameTaken`). **A new version resubmits
  itself:** when the author saves a new version (form, archive, import or
  the room tools) of a component the organization uses (an active org row)
  or with a request pending, the gateway opens the request for it (one
  pending per component: it supersedes the older one; the admins are
  notified only when none was pending) and the save's reply carries it as
  `resubmitted`; the web tells the author, `save_component` tells the
  agent. An unpublished or withdrawn publication is not resubmitted. Admins unpublish
  (`retired`: hidden from the Library and new use; rooms that pinned it
  keep their version), restore or remove it (rows and uses deleted); the
  author's `publish {scope: "personal"}` withdraws a pending request and
  unpublishes the org row. The author's Library shows the publication on
  their own component (`publication`: the org version and status, the
  latest request), not as a second card.
- **Platform scope** is the existing signed install. A super admin exports
  any stored version (`GET /v1/platform/content-components/{id}/package`,
  `bnbloc content export`) as a `pkgmeta` package of the content kind:
  runtime `bundle`, one artifact `bundle` = the version's bundle on the
  gateway (`/v1/artifacts/sha256/<hex>`), pinned by sha256
  (`contentpkg.PlatformPackage`; every content kind registers this payload
  validator with `pkgmeta`). It is signed offline and installed through
  `POST /v1/platform/components` (`bnbloc cores install --package …
  --signature …`, or `bnbloc content promote`), which recognizes a content
  kind: `extpipe.Pipeline.VerifyPackage` checks the signature against the
  trusted keys, the gateway reads the pinned bundle from the artifact
  store, validates it again and requires the version the package names,
  then creates or moves forward the platform row (no tenant, no owner) with
  the signature on the version. Reinstalling a version with the same bundle
  is a no-op; other content under an installed version is refused. Rollout
  `off` installs it unpublished; `optin` is refused. **Tenant scope:** the
  component route `PUT /v1/platform/components/{name}/tenants` limits a
  platform row to some organizations with its fail-closed semantics (an
  empty list is a 400, `{"allTenants": true}` lifts the limit, both is a
  400); `?kind=expert|skill|prompt` picks platform content, and without a
  kind a name held by both an executable record and platform content is a
  409. The console's Platform content page edits it ("Organizations").
- **Notifications** (the gateway's emitter): `content_shared` to each
  recipient whose copy was created or moved; `content_review` to the
  organization's admins for a request and to the author for the decision.
- **Audit** (`extension_audit`, kind `content`): `share` (also a copy given
  on join), `unshare` (a room share revoked), `update`, `request` (naming
  the agent when the room tools asked), `approve`, `decline`, `unpublish`,
  `restore`, `remove`, `install`, `set-tenants`.

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
   `store.LoadLeasedJob`: the job is live and leased by the caller's
   bridge). `contentuse.Effective` returns the job's expert at its pinned
   version, the skill packs the expert requires (highest version satisfying
   `name@range` among what the requester may use and the room's packs),
   the room's skill packs and the agent owner's own skill packs — each
   once — plus unmet requirements.
3. **Download.** Each bundle is fetched from `GET /v1/artifacts/{key}?job=&room=`
   (`artifactEntitled` admits a bundle in effect for a job the caller's
   bridge runs), verified against its sha256 and validated again.
4. **Profile.** A core advertising `agentProfile` receives the profile
   (instructions + skills + the MCP tools the content requires + the
   session contributors' MCP servers) on
   session create and maps it natively (see
   [`core-providers.md`](core-providers.md)). Any other core gets the
   fallback: the instructions and a skill summary prefixed to the session's
   first prompt, the skill files under `.agents/skills/` when it accepts
   workspace files, the bodies inlined otherwise.
5. **Sessions.** The profile's content checksum (instructions + skill
   files; MCP servers and env excluded), plus the MCP tool set's non-secret
   identity, is part of the session binding: a turn wearing different
   content or tools starts a new session; a reused sandbox workdir has its
   agent-config paths cleared first.
6. **Room expert.** A room's expert (a `room-expert` use) is resolved at
   turn time like room skill packs: `Effective` returns it as `roomExpert`
   (with the skill packs it requires, its bundle granted) unless the chat
   picked it, and reports it missing once the room no longer pins it. The
   bridge puts its instructions first and the chat expert's after; the
   running job's diagnostics name both (`roomExpert`, `expert`). The
   gateway records it on the job when it answers the resolution
   (`room_expert_id/version/name`, `store.RoomExpertRecorder`), so the run
   record carries `roomExpert` beside `expert`, and appends the same
   `content`/`use` audit row the chat's expert gets ("room expert House
   style v1.2.0 on agent jack/acme/codex (job …, room …)") when the
   recorded expert changes (a retried resolution records nothing new).
7. **What is missing** (an unmet requirement, a room expert gone) is in the
   running job's `contentMissing`; the live job line shows it ("Running
   without …").

## Expert teams

A team is an expert with a `team` block (`internal/contentpkg/team.go`):

```json
"team": {
  "lead": "coordinator",
  "members": [
    {"role": "writer", "expert": "haiku-writer@^1", "defaultAgent": "requester", "description": "Writes haiku."},
    {"role": "critic", "expert": "haiku-critic@^1", "defaultAgent": "member:maya/opencode-b"}
  ]
}
```

- `lead` is the lead's title (display; default `lead`). The lead is the
  agent the requester addresses with the team picked; the team expert's
  `INSTRUCTIONS.md` is its SOP.
- 1 to 8 roles (`wire.MaxDispatchFanOut`): `role` (`[a-z0-9-]`, unique,
  not the lead's), `expert` (`name@range`), `defaultAgent` (`requester` —
  the requester's own agent, `platform` — a platform agent added to the
  room, `member:<id>[/<provider>]`), optional `description` (what the role
  covers).
- **Experts resolve as the requester** (`contentuse.ResolveTeam`, in
  `Effective`'s `team`): the highest version satisfying the range among the
  experts they may use and the room's pinned ones. A member expert that is
  itself a team is refused (`missing`). Member experts are not granted to
  the lead's bridge; each member's bridge resolves its own job's expert.
- **Agents are staffed** by `internal/expertteam.Resolve` from the room's
  addressable agents (activity's rule for the agent catalogs: members' live
  agents plus the room's available platform agents), never on the lead's
  own agent; several candidates pick the first by owner, core, provider.
  Activity serves it as `POST /v1/rooms/{room}/team-plan` (below).
- **Pre-flight estimate** (`expertteam.Estimate`): the lead plus every
  staffed role once, each at its agent's average reported `costUsd` over
  its last 20 replied runs in the organization; seats without a reported
  cost are named (`unknown`), never guessed; `runs` counts each agent's
  sample once. The composer shows it when a team is picked and holds the
  send until the member confirms.
- **Run time** (bridge): the lead's bridge asks for the plan with
  `?job=`, appends the generated roster section (`expertteam.Section`:
  role → expert → agent handle, online or why not; the capability, progress
  and failure rules) after the SOP, and binds the roster to the turn's
  room tools session (`TurnInfo.Team`). The roster is part of the content
  checksum. A team on a core without `agentProfile` is refused with a
  notice (no room tools, no dispatch). The guards live in the room tools
  ([`room-tools.md`](room-tools.md), "Teams").

`POST /v1/rooms/{room}/team-plan` (activity) takes `{lead?: {ownerId,
core?, provider}, members: [{role, defaultAgent}]}` and answers
`wire.TeamPlanReply {lead, roles: [{role, defaultAgent, agent?, online,
reason?, cost?}], estimate: {costUsd?, runs, unknown?}}`. Without `?job=`
the caller is the requester and `lead` is required (a room member); with
`?job=` the job must be in the room, live and leased by a bridge of the
caller (`store.LoadLeasedJob`), and the lead and requester are the job's.

## MCP tool components

Design: PRD §6 and "Status: step 6 (MCP tool components)". An MCP tool
(`bnbloc.mcp.package/v1`, contract `bnbloc.mcp.contract/v1`) is an
**executable** kind: platform-signed, installed by a super admin through the
shared component pipeline (`POST /v1/platform/components`, the console's
Components page, `bnbloc cores publish-mcp`), with rollout, tenants and
opt-in unchanged. It is the admin-approved tool catalog: content requires a
tool by name and can never inline a command.

```json
{"kind": "bnbloc.mcp.package/v1", "contract": "bnbloc.mcp.contract/v1",
 "name": "codeword", "version": "1.0.0", "publisher": "acme", "runtime": "stdio",
 "artifacts": {"darwin-arm64": {"url": "…/v1/artifacts/sha256/…", "sha256": "…"}},
 "mcp": {"tools": [{"name": "get_codeword", "description": "…"}],
         "env": [{"name": "CODEWORD_SALT", "description": "…", "secret": true, "required": true}],
         "credentials": "member",
         "stdio": {"args": ["--stdio"]}}}
```

- **Runtimes.** `stdio`: one binary per platform (`artifacts`, staged
  through `POST /v1/platform/artifacts` and pinned by sha256 like sidecar
  binaries); the artifact is the command, `mcp.stdio.args` its arguments.
  `endpoint`: `endpoint.baseUrl` is the MCP (streamable HTTP) URL, held to
  `permissions.network`; `mcp.http.auth` is `none`, `bearer` or
  `header` (`{scheme, header?, env}`), the value read from its one declared
  variable. The install probe (`internal/mcptool`, through
  `internal/endpointpkg`'s `Discover` hook) posts MCP's `initialize` and
  accepts a JSON-RPC answer or 401/403. Nothing runs server-side;
  activation registers nothing.
- **Assembly.** `pkgmeta.PrepareMCP` (before any upload: the kind and
  contract filled in, stdio needs at least one platform, an endpoint none)
  and `pkgmeta.AssembleMCP` (the staged binaries pinned, then validated)
  are the one MCP package assembly. `bnbloc cores publish-mcp` uses them
  and so does the super-admin `POST /v1/platform/mcp-packages`, which the
  console's "Build an MCP tool" form posts to: name, version, publisher,
  tools, env (secret, required), credentials source, and either stdio
  binaries per platform (staged through `POST /v1/platform/artifacts`,
  sent as `artifacts: {<platform>: {key}}`) with `args`, or an http URL
  with its auth and network hosts. The route answers the exact package
  JSON to sign; signing stays offline (`bnbloc cores install --package
  <file>` signs with the publisher key and installs, or a detached
  signature goes with the file into Components → Publish).
- **Credentials.** `mcp.env` declares the variables (names, secret,
  required, a description); `mcp.credentials` says where values come from:
  - `member`: each member enters them on their desktop (Settings → Tools),
    stored 0600 under the app config dir through the plugin settings store
    (`internal_ext/mcp_tools.rs`), never sent to the gateway. The bridge
    reads them (`BNBLOC_MCP_SETTINGS_DIR/<tool>.json`) only for drives the
    member asked for themselves: a borrowed or platform drive never lends
    the agent owner's accounts (the tool shows missing).
  - `oauth` (`mcp.http.auth: {scheme: "oauth", clientId?, scopes?}`,
    endpoint tools only, no `env` and no org connector): each member signs
    in on their desktop (Settings → Tools). The desktop
    (`internal_ext/mcp_oauth.rs`) runs the MCP authorization flow against
    the tool's own server: protected resource metadata (the 401
    `WWW-Authenticate resource_metadata`, else the well-known URIs; its
    `resource` must be the tool's URL), authorization server metadata
    (RFC 8414, then OIDC discovery, in the specification's order; `issuer`
    checked; refused without PKCE S256), the package's `clientId` else
    dynamic registration, PKCE, `state`, RFC 9207 `iss` and `resource`
    (RFC 8707) on a 127.0.0.1 loopback redirect. Tokens are stored 0600 in
    `<mcp-settings>/oauth/<tool>.json`, refreshed before expiry (rotating
    the refresh token) and forgotten with the tool or when a refresh is
    refused. The bridge sends the access token as `Authorization: Bearer`
    on the member's own drives only; a missing or expired sign-in is
    `contentMissing`. Nothing about the authorization server is declared
    or stored server-side.
  - `org`: an org admin configures one connector per (organization, tool)
    at `/v1/tenant/mcp-connectors` (Organization → Tool connectors). Values
    are sealed with AES-256-GCM under `BNBLOC_SECRET_STORE_KEY`
    (`internal/sealbox`; the platform-settings key
    `mcp.connector/<tenant>/<tool>` is the additional data, so a row moved
    to another organization does not open), opened only into the effective
    resolution of a job of that organization, and deleted with the tool.
    Without the key connectors are off.
- **Distribution (stdio).** The member catalog (`GET /v1/components`, kind
  `mcp`) carries the pins; artifacts are entitled like every catalog
  kind's. The desktop's sidecar manager does not supervise a tool: for each
  installed stdio tool it fetches this platform's binary through the shared
  verified download (`ensure_verified`, the sidecar cache naming
  `mcp-<tool>-<sha256>`) into the app cache's `mcp-tools/`, writes
  `index.json` (`{tools: [{name, sha256, path}]}`) atomically, and deletes
  binaries of tools that left the catalog or changed pin (their member
  credentials are forgotten too). The bridge gets `BNBLOC_MCP_TOOLS_DIR`
  and reads the index per session; nothing restarts. A just-published tool
  appears within seconds: a catalog change pushes `components_changed` on
  the member stream and the desktop refreshes its catalog then (the 60 s
  poll is the safety net).
- **Use time.** `GET …/effective` lists, for every `requires.mcp` of the
  expert, the room expert and the skill packs in effect, the installed
  tool the requester may use (`extpipe.MemberVisibility`): transport,
  per-platform pins and args, or URL and auth; its env (required when
  declared required or named by a requiring package); an org tool's values;
  and the ids of the packages requiring it. Unknown, invisible,
  unconfigured or undeclared-variable requirements go to `missing`. The
  bridge (`services/bridge/mcp_tools.go`) builds the session's MCP servers:
  a stdio server from the index entry carrying the gateway's pin for this
  platform (inside the tools dir) with the env values as its environment;
  an http server with its URL and auth header. A tool not downloaded yet,
  member credentials not set (or not lent), a core without `agentProfile`
  or a target without http MCP joins `contentMissing`; the turn still runs.
  Someone else's content requiring a stdio tool forces the sandbox. Values
  never reach logs, the session checksum or job data.

## Safety

- Declared permissions show on cards and are confirmed on first use
  (`POST …/versions/{v}/ack`; the web asks before a member first sends with
  someone else's expert, a shared copy included). Org admins see them, the
  files that run and the MCP tools required before approving.
- Content that is not the agent owner's own (its author, not the holder of
  a copy, decides) and declares `scripts` or `mcp.stdio`, or requires an
  MCP tool that resolves to stdio, forces a sandboxed drive
  (`decideSandbox`), and is refused with a `sandbox-required` notice where
  the sandbox cannot take it.
- MCP servers come only from platform-signed MCP tool components; content
  names them and can never inline a command or URL. A member's own tool
  credentials are used only for drives they asked for themselves.
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
| `GET /v1/content-components?kind=expert\|skill\|prompt` | The caller's Library (`contentuse.Visible`): their own components and the copies they hold plus org/platform ones visible to them, at the latest version, with usage counts (`runs`, `members`), a team's roster (`team`), `mine`/`removable`, a copy's `sharedBy`/`sourceId`/`sourceLatestVersion` and the author's `publication` |
| `POST /v1/content-components` | Save a new component: `{form, version?}` (JSON) or a package archive (`component.json` names it). 201 detail; 409 duplicate name; 400 validator message. With `?job=&room=`: a save by the bridge running the job, as its requester (below) |
| `POST /v1/content-components/import?kind=&name=` | Import an archive (tar, tar.gz, zip) or a single markdown/json file of prompts as a new component; `?id=` imports a new version of the caller's component |
| `POST /v1/content-drafts?job=&room=` | The bridge running a job stages an unsaved expert or skill pack for a quick try (archive body; as the job's requester; 201 `wire.ContentDraft`); see [room-tools.md](room-tools.md), "Quick tries of drafts" |
| `POST /v1/content-components/prompts` | "Save as prompt": `{title?, body, tags?}` into the caller's personal prompt pack (created on the first save); 201 `{component, promptId, created}` |
| `POST /v1/content-components/{id}/prompts/{promptId}/use[?room=&version=]` | Count one insertion of a pack's prompt (204) |
| `GET /v1/content-components/{id}[?room=]` | Detail with versions (readable when visible, or used by a room of the caller's) |
| `DELETE /v1/content-components/{id}` | Delete (the author's own, or a copy the caller holds) |
| `POST /v1/content-components/{id}/shares` | `{members?, roomId?, version?, note?}`: copies for named members of the author's organization and/or a room's human members, and a standing room share; `{version, shared: [{member, componentId, version, status: created\|updated\|unchanged}], skipped: [{member, reason}], room?}` (the author only) |
| `GET /v1/content-components/{id}/shares` | The copies made from the author's component and its room shares (`{copies: [{member, componentId, version, updatedAtMs}], rooms: [{roomId, version, note?, createdAtMs, updatedAtMs}]}`) |
| `DELETE /v1/content-components/{id}/shares/rooms/{room}` | Revoke a room share: later joiners get no copy, existing copies stay (the author only; 404 when there is none) |
| `POST /v1/content-components/{id}/update` | A shared copy moves to its author's latest version (its holder; 409 when the author no longer has it) |
| `POST /v1/content-components/{id}/publish` | `{scope: org\|personal\|platform, version?, note?}`: `org` opens a publish request (201 `{scope, request, note}`); `personal` withdraws it and unpublishes the org row; `platform` is a 403 saying what a platform admin does (the author; with `?job=&room=` as the job's requester) |
| `GET /v1/tenant/content-requests[?status=all]` | Org admins: publish requests of the organization (pending by default) with the approved version |
| `GET /v1/tenant/content-requests/{id}` | Org admins: the review (`wire.ContentReview`: the requested version, `scriptFiles`, `previous`, `files` changes) |
| `POST /v1/tenant/content-requests/{id}/approve\|decline` | Org admins: `{note?}`; approve publishes the version to the organization |
| `GET /v1/tenant/content-components` | Org admins: the organization's published content, any status |
| `POST /v1/tenant/content-components/{id}/unpublish\|restore`, `DELETE /v1/tenant/content-components/{id}` | Org admins: unpublish, restore, remove |
| `GET /v1/platform/content-components[?scope=platform\|org]` | Super admins: platform content and every organization's, any status (a platform row with its `enabledTenants`) |
| `PUT /v1/platform/components/{name}/tenants?kind=expert\|skill\|prompt` | Super admins: `{enabledTenants: [...]}` or `{allTenants: true}` for platform content (the component tenant scope route) |
| `GET /v1/platform/content-components/{id}/package[?version=&publisher=]` | Super admins: the signable package of a version (install it with `POST /v1/platform/components`) |
| `POST /v1/platform/content-components/{id}/unpublish\|restore`, `DELETE /v1/platform/content-components/{id}` | Super admins: platform content |
| `POST /v1/content-components/{id}/versions` | New immutable version: `{form, baseVersion, version?}` (the base's extra files carry over) or an archive; owner only; strictly newer. The detail carries `resubmitted` (the publish request it opened) when the organization uses the component |
| `GET /v1/content-components/{id}/versions/{v}[?room=]` | The version: component as of it, instructions, skill bodies, files (text when viewable) |
| `POST /v1/content-components/{id}/versions/{v}/ack[?room=]` | Confirm the version's declared permissions |
| `GET /v1/content-uses[?room=]` | The caller's member uses and a room's uses, each with its component at the version in effect and `updateAvailable`; `roomExpert` names the room's expert |
| `PUT /v1/content-uses` | `{scope: member\|room\|room-expert, roomId?, componentId, version?}`: member scope takes the kinds with `MemberUse` (skill and prompt packs); room scopes need the room's owner or an admin; `room-expert` takes one of the room's pinned experts (never a team) and replaces the previous one |
| `DELETE /v1/content-uses?scope=&roomId=&componentId=` | Remove a use (unpinning the room expert clears it) |
| `GET /v1/content-components/effective?job=&room=` | The bridge running a job: what its turn wears, with the MCP tools its content requires (`mcp`, an org tool with its connector's values) |
| `GET /v1/components?kind=mcp` | The MCP tools visible to the caller (rollout, tenants, opt-in) with their tools, env, credentials source and, for org credentials, `orgConfigured` |
| `GET /v1/tenant/mcp-connectors` | Org admins (`manage_org`): the org-credential MCP tools visible to the organization with their connector's state (secret values never returned); `available: false` without the secret store key |
| `PUT /v1/tenant/mcp-connectors/{name}` | `{values}`: store the organization's connector (an empty secret keeps the stored one; required variables must end set); 409 without the secret store key |
| `DELETE /v1/tenant/mcp-connectors/{name}` | Remove the organization's connector |
| `GET /v1/artifacts/{key}[?room=\|?job=&room=]` | Bundle download (`artifactEntitled`) |

Every change of a room's uses (a pin, the room expert, a room pack)
publishes a transient `content_uses_changed` room event (`{scope,
componentId}`) through realtime's internal publish; the composer's experts
and the room settings refetch the room's uses on it. A save or a publish
request made on behalf of a job (the room tools) publishes a transient
`content_saved` event in the job's room (`wire.ContentSaved`:
`{componentId, kind, name, displayName?, version, action: saved|submitted,
created?, owner}`); the member it was saved for gets the save toast ("Open
in Library", "Try it now") from it. Nothing re-lists the Library to find a
save.

**Requests on behalf of a job's requester.** The room tools
(`save_component`, `get_component`, `list_components`, `publish_component`,
`try_draft`; see [`room-tools.md`](room-tools.md)) call
`POST /v1/content-components`, `POST …/{id}/versions`, `GET …/{id}`,
`GET …/{id}/versions/{v}`, `POST …/{id}/publish`,
`GET /v1/content-components` and `POST /v1/content-drafts` with a
`?job=&room=` grant
(`services/gateway/content_requester.go`). The grant follows dispatch's
authority model: it is admitted only while the job is live and leased by a
bridge of the caller (`store.LoadLeasedJob`) and the job's requester is still a
human member of the room; the request then acts as the requester (their
Library, personal scope, owner = requester). When the agent is not the
requester's own (borrowed or platform), a package that ships scripts is
refused (403): someone else's machine may not plant executable content
into a member's own Library, where own content runs unsandboxed. Every
granted save is an `extension_audit` row (kind `content`, action `save`,
actor = requester, detail naming the agent and the bridge); a granted
publish is a `request` row naming the agent. A staged draft is not saved:
it is no component, and the try's dispatch is audited as a content `try`
(the draft's author, the staging agent's owner, is its trust owner). No
other route honors the grant (sharing and the admin routes never do).

**Saving deliverables.** Without room tools the drafted files are ordinary
deliverables: the web offers "Save as expert" / "Save as skill pack" /
"Save as prompt pack" under a turn's files when they include a
`component.json` of a content kind, packs
the room's latest files under that directory as a tar and calls
`POST /v1/content-components` (or `…/{id}/versions` for one of the member's
own) as the clicking member; the validator's message shows inline.

`GET /v1/components` also lists the member's content components through the
same visibility function, in a separate `contentComponents` array (the
desktop's executable catalog decodes `components` strictly; nothing of a
content component runs as a supervised process).

**The Library as an agent reads it.** With a `?job=&room=` grant
(`contentGrant`, honored like the content routes) `GET /v1/components` is
the job requester's Library: `contentComponents` (`contentuse.Visible`) and
only the MCP tools of `components` (`extpipe.MemberVisibility`, each with
`publishedAtMs`, when the platform installed that version), never the
executable catalog a desktop supervises. `roomlog.ReadLibrary` is its one
reader: the room tools' `list_components` and the bridge's
`.room/library.md` (room-tools.md, "What every agent is told"). Nothing on
the agent side re-derives visibility.
