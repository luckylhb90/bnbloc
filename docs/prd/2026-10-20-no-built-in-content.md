# No prompts in the platform: built-in content becomes default content (2026-10-20)

Status: proposed. Owner: the content store and its scopes
(`internal/contentbuiltin`, `internal/contentuse`, `services/gateway/content_platform.go`),
the assist resolution (`internal/assist/tasks.go`), the CLI.

The platform binary ships no prompts. Its 22 built-in content packages
(13 assists, 2 experts, 2 skill packs, 5 prompt packs, ~4,900 words of
assist instructions plus two skills of ~1,650 words) move to their own
repository and are published to every organization as ordinary
platform-scope content: the **default content**. Anyone can copy and change
it; an organization can replace any of it with its own. The platform keeps
the capabilities (the assist route and its output contracts, the content
kinds, the room tools and their short briefs).

## 1. Why

- The owner, 2026-10-20: "这套机制是不是提前写死了很多prompt在项目代码里？"
  It does. The packages are embedded with `//go:embed` and read-only: an
  organization cannot change a word, a member's copy is refused for the
  consent assist, and changing a prompt needs a platform release.
- `docs/design-review.md` lens 14 ("Capabilities, not use cases", promoted
  2026-10-20) says a use case is never platform code or built-in content.
  Built-in experts, skills, starter prompts and the assists' prompts are
  use cases: how to build an expert, how to judge a dead end, how to
  describe a room.
- What stays platform: the agents' tool briefs and session guidance (the
  contract for calling platform capabilities, budgeted in
  `services/bridge/prompt_test.go`), the action buttons a card offers, and
  each assist's **output contract** (its task name and fields), which the
  web and gateway validate.

## 2. What it is (the end state)

### 2.1 Default content, published like any content

- A repository `bnbloc-default-content` holds the 22 packages, unchanged in
  format. Its CI validates them against a pinned platform version
  (`contentpkg.Validate`) and runs the contract checks that today live in
  `internal/contentbuiltin` (§3.4).
- A release installs them at platform scope with rollout `auto` (every
  member of every organization, as today) through a new CLI path:
  `bnbloc content publish --dir <package> --rollout auto` (signed like
  `content promote`; today promote can only start from a component already
  stored in some organization).
- A new version of a package adds a version to the same platform row;
  rooms and jobs pinned to an old version keep resolving it (today a
  release that bumps a built-in breaks every pin to the old version).

### 2.2 No compatibility (owner, 2026-10-20: "不用考虑兼容，直接按新逻辑改")

- The default content is installed as ordinary platform content with new
  ids. Stored references to `cc_builtin_<name>` (a room's built-in expert
  or packs, favorites, prompt counters, permission acks, a queued job's
  expert, work recipes) are not mapped: they resolve to nothing, as a
  deleted component does today (a room's expert is unset, a recipe part is
  skipped). Pre-launch: no backfill, no id rule.
- The web finds the Expert builder by name; nothing names a built-in id.

### 2.3 Assists resolve to the nearest version, with one exception

- Already true: `POST /v1/assist/{task}` runs the nearest installed assist
  of that name (personal, shared, org, platform). A member may replace
  `enhance-prompt` for themself; an organization for its members.
- The exception: an assist the gateway runs for consent
  (`explain-source-use`, today `runBuiltinJSON`) resolves organization,
  then platform, never a member's own copy (an admin decides the grant,
  so only an admin's version may explain it).
- The platform owns each assist's output contract (task name, required
  fields and types, the gate's presence): a package that does not meet it
  is refused at save for that name, so a replaced prompt can change the
  wording, never break a dialog.

### 2.4 The web stops depending on the built-in scope

- Starters come from prompt packs marked as starters (their manifest),
  whatever their scope, not from `isBuiltin`.
- The Expert builder is found by name (`expert-builder`), not by a fixed id.
- "Built-in" badges become "Default" for platform content published by the
  platform's operators.
- No assist installed for a task: its button is hidden (already the
  behaviour, `useAssistTask`), the manual path stays.

### 2.5 The platform publishes its contracts as data, content links them

- Content that copies platform facts today (the interactive-apps skill
  copies the app SDK's types and limits; the expert builder's package
  format carries a generated limits table) reads them from the platform
  instead: the room already gets the app SDK at `roomlog.AppSDKPath`; the
  content limits are served the same way (a file in the room's `.room/`
  docs, generated from `contentpkg.Limits`). The content refers to those
  files; nothing is copied, so nothing drifts.

### 2.6 What agents still know (the owner's concern)

The owner: without those prompts, do agents still know the capabilities,
and does the experience get worse? Where an agent's knowledge comes from
(survey 2026-10-20, `services/bridge/profile.go` `sessionGuidance`,
`services/bridge/prompt_test.go` budgets):

| Layer | What it tells the agent | Where it lives | Moves? |
|---|---|---|---|
| What it can do | session guidance (~745 tokens), every room tool's brief (`tools/list`, ~2,630), `.room/README.md`, `room_help` | platform code | **no** |
| Platform facts | the app SDK (`roomlog.AppSDKPath`), content limits (§2.5) | platform, served as files | no (the copies in content are removed) |
| How to do it well | an expert's instructions, skill packs (interactive apps, media), starter prompts | built-in content | yes, to default content |
| Buttons' AI | the 13 assists behind the web's AI buttons | built-in content | yes, to default content |

- Every session gets the first two layers whether or not any content is
  installed; the third reaches an agent only when the room or the message
  uses that expert or pack (nothing attaches the built-in skills
  automatically today). So moving content does not change what an agent
  knows about the platform's capabilities.
- The default content is the same bytes, installed for everyone: the
  experience is unchanged where it is installed. Where it is not (a fresh
  deployment before its install step, §5), the AI buttons are hidden and
  the Expert builder is absent; agents still work.
- Guard: a test suite of real tasks (build an app with the SDK, make a GIF
  guide with the browser, set up and save a room by talking, find a way
  past a dead end) runs against a deployment with the default content and
  one without, so a regression in what agents know shows as a failing
  task, not a complaint.

## 3. Feasibility

### 3.1 What the code depends on today (survey 2026-10-20)

| Dependency | Count | Effort |
|---|---|---|
| Go non-test code naming a built-in | 1 (`explain-source-use` via `runBuiltinJSON`) | small: resolve org > platform |
| Web: task-name constants (`useAssistTask(NAME)`) | 12 | none: names stay |
| Web: id or scope dependent (`BUILDER_EXPERT_ID`, `starterChoices` via `isBuiltin`, badges) | ~10 places | small |
| `requires` between packages (`app-builder` → `interactive-apps`, `app-starters`; `media-skills` → `media-tools`) | 3 | none: by name |
| Stored ids `cc_builtin_<name>` | 7 tables/fields | none: not mapped (§2.2) |
| Go tests tied to built-ins | ~150 references, mostly `internal/contentbuiltin` | medium: move to the content repo, keep contract tests here |
| Docs assuming built-ins | ~10 | small |

### 3.2 Missing capabilities

- Installing platform content from a directory (§2.1): the pieces exist
  (`POST /v1/platform/artifacts`, `contentpkg.PlatformPackage`), not
  assembled.
- Starter flag on prompt packs, if the manifest lacks one (check
  `TestStartersShipAsStarterPromptPack`).
- Contract files served to rooms (§2.5).

### 3.3 Effort

Medium: one CLI command, one resolution exception, ~10 web places,
moving the packages and their tests, docs. No data migration.

### 3.4 Tests that move and tests that stay

- Move to the content repo: per-package budgets, field checks, starters,
  goldens.
- Stay in the platform: each assist task's output contract,
  the resolution order and the consent exception, "the binary embeds no
  content package" (an architecture test replacing `TestOneStarterMechanism`).

## 4. Effect on members (the owner's question)

| Who | Today | After | Visible change |
|---|---|---|---|
| A member using AI buttons | built-in assists | the same packages, platform scope, same names | none |
| A room whose expert is the Expert builder, favorites, prompt counters | `cc_builtin_*` ids | not mapped (§2.2) | that room's expert is unset; favorites and counters of built-ins reset |
| A job or room pinned to an old built-in version | breaks on each release bump | keeps resolving (versions are stored) | fewer failures |
| An organization that wants its own prompts | impossible | publish its own version under the same name | new ability |
| The Library | "Built-in" badge, read-only | "Default", copyable | label |
| A fresh deployment | content in the binary | one install step (§5); until then AI buttons hidden, no starters | operator step |

Risks:
1. **Order of deploy.** If the platform drops the built-ins before the
   default content is installed, AI buttons disappear and the Expert
   builder room loses its expert until it is. §5 avoids it.
2. **Content drifts from the platform** (an SDK change the skill does not
   know). §2.5 removes the copies; the content repo's CI pins a platform
   version and runs the contract checks.
3. **An organization's replacement is worse.** It is theirs, scoped to
   them; the default stays one click away (delete theirs).
4. **The consent explanation replaced by a member.** Never: §2.3.

## 5. Rollout

1. Platform: the CLI install path, the consent exception, the web changes,
   and `internal/contentbuiltin` deleted (overlay, embed, read-only paths,
   the builtin scope); an architecture test keeps the binary free of
   content packages.
2. The same day on each deployment (QA first): publish the default content,
   then check every AI button, the starters and the Expert builder.
3. Docs: deployment checklist (install the default content), AGENTS.md
   content rules, `content-components.md`, `ai-assist.md`, and supersede
   the line of `docs/prd/2026-10-18-components-in-own-repos.md` that kept
   built-ins in the main repo.

## 6. Design review

| Lens | Answer |
|---|---|
| Component or platform | Prompts and content leave the platform; it keeps the assist route, output contracts, kinds, tool briefs. |
| One owner | Content in its repo; the consent exception in one place; contracts served from their owners (§2.5). |
| Boundaries and privacy | The consent assist is never a member's; organization replacements reach only that organization. |
| Defaults and unknowns | No assist for a task: the button is hidden and the manual path works. |
| Release and compatibility | No compatibility (pre-launch, the owner's call): new ids, one step, default content installed the same day. |
| Load on users and agents | None new; agents' briefs unchanged. |
| AI capability | Unchanged features; organizations can tune their AI. |
| Cost | None at runtime. |
