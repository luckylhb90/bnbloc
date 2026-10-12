# A template's own starter list, and use counts shared across organizations (2026-10-13)

Status: open. The two follow-ups of
`docs/prd/2026-10-13-builder-format-skill-and-builtin-starters.md`.

## 1. A second starter mechanism in room templates

### Symptom

A prompt pack's prompts flagged `starter` are the one starter mechanism
(`starterChoices`: the room's packs, then the built-in `starters` pack). A
room template still carried its own list: `RoomTemplateBody.starterPrompts`
(free text, bounded by `wire.MaxStarterPrompts`/`MaxStarterPromptBytes`),
picked from recent messages in "Save as template" or typed in the Library's
template editor, returned once in the instantiate reply and shown by
`TemplateStartCard` from local storage. Two mechanisms for one thing: the
template's starters were never prompts (no use counts, no slash command,
no variables), they vanished once the card was dismissed, and nothing
stopped a third list from being added.

### Root cause

Templates predate prompt packs; when starters moved into packs the
template's list was not folded in.

### Fix

- `RoomTemplateBody.starterPrompts` is gone, with its limits. A template
  names prompt packs instead: `promptPacks` (`componentId`, optional pinned
  `version`), the shape of a room content use.
- The room service reads content through the built-in overlay, like the
  gateway and realtime. "Save as template" captures the room's prompt-pack
  uses (server-owned, like the channels); creating or editing a template
  checks each pack is a prompt pack the author may use
  (`contentuse.Usable`). Instantiating puts each pack the creator may use
  as a room content use of the new room (`PutContentUse`, room scope);
  one the creator may not use is reported in `failedPromptPacks`, never
  fatal. Its starters then show through `starterChoices` like any room's.
- The instantiate reply drops `starterPrompts`; `TemplateStartCard` keeps
  the suggested-agent and member follow-ups only.
- Storage: the list was a key of `room_templates.body_json`, not a column,
  so there is no column to drop. Nothing reads the key any more (JSON
  decoding ignores it) and, pre-launch, no stored template is rewritten.
- `TestOneStarterMechanism` fails on a starter field in a wire/store JSON
  tag, a starter column in `schema.sql`, or a starter list literal in the
  web.

## 2. Use counts of shared content leak across organizations

### Symptom

`content_prompt_uses` had one row per (component, prompt). Built-in and
platform prompt packs are used by every organization, so a member of one
organization saw prompt use counts that included other organizations'
insertions. The pack and expert counts of `content_component_uses`
(`CountContentUses`, `CountMemberUses`) counted every organization's rows
of a platform or built-in component the same way. A built-in pack's
prompt use was refused outright (`ErrContentNotFound`: the store checked
for a component row, which built-in content never has), so the `starters`
pack's prompts were never counted.

### Root cause

The content store is not tenant-scoped by the context (content ids are
global), and these reads and writes were marked `cross-tenant` instead of
carrying the caller's tenant.

### Fix

- `content_prompt_uses` gains `tenant_id` in its primary key. Every write
  and read carries the tenant: `RecordContentPromptUse(ctx, tenant, …)`,
  `ContentPromptUses(ctx, tenant, …)`, `CountContentUses(ctx, tenant, …)`,
  `CountMemberUses(ctx, tenant, …)`; the gateway passes the caller's
  (acting) tenant.
- A built-in prompt use is accepted once the overlay vouches the built-in
  exists (as `PutContentUse` already did).
- `idx_content_uses_component` becomes `(component_id, tenant_id,
  scope_kind)` so the bounded per-tenant counts stay on the index.
- Migration `2026_10_content_use_counts_tenant`: the old counters cannot be
  attributed to an organization (that is the leak), so a table without
  `tenant_id` is recreated empty; no backfill.
- Contract `TestContractContentComponentStoreTenantUseCounts` on both
  backends: organization A's prompt uses, member uses and scope uses of a
  platform and a built-in component never show in organization B's counts.
