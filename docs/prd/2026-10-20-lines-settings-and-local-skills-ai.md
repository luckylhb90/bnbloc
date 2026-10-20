# A list setting, local skills' own settings, and their AI (2026-10-20)

Status: built 2026-10-20 (platform commits below; the component's on the
`settings-and-ai` branch of `luckylhb90/bnbloc-local-skills`, version
0.2.0, not published). Owner: the component settings contract
(`core/pkgmeta`, docs/prd/2026-10-19-component-settings.md), the
skills-provider capability's panel (web), the assist seam
(`internal/contentbuiltin`, `lib/assist.ts`).

## 1. What and why

Local skills kept its folder lists and secret rules in a policy file a
member had to write by hand, and the settings contract had no way to say
"a list": every value was one string. Its panel lived in Settings → Skills,
apart from the component's card where everything else about a component is.
Three changes, each with its AI part:

1. **A `lines` setting type** in the contract: a list of text lines
   (folders, glob excludes, names). Bounds owned once
   (`wire.MaxComponentSettingLines` = 64 lines,
   `wire.MaxComponentSettingLineBytes` = 512 bytes a line; instead of one
   value's 4 KiB), checked by `pkgmeta.CheckSettingValue` (each line
   trimmed and non-empty) and its desktop copy, pinned to
   `core/pkgmeta/testdata/setting_values.json` (so is the web's form
   helper).
   - *Stored and sent* (the stores, the wire, the resolved and endpoint
     answers, a save): its lines joined by "\n", so every store and route
     keeps `map[string]string`.
   - *Delivered*: in the settings file (`BNBLOC_COMPONENT_SETTINGS_FILE`,
     `pkgmeta.SettingsFile`, written by the bridge and the desktop) a JSON
     array of strings; as a `deliver.env` value, newline-joined (an env var
     is a string); an endpoint component's ask answers it as stored.
   - *Edited*: the one form renders a textarea, one per line
     (`lib/componentSettings.ts`: `parseLines` trims each line and drops
     empty ones, `linesProblem` says the bound first); a settings page gets
     and sets a list of strings (the protocol's `SettingValue`, the host's
     `storedValues`).
2. **Local skills declares its settings** (its own repository): member
   `extraSkillDirs` and `excludes`, organization `secretNames`,
   `secretPatterns` and `maxSkills`. The shipped policy stays the base; the
   settings file's values are merged onto it by the one merge
   (`Policy.Merge`): a setting adds, never removes. Its `policyFile` and the
   member's `agentDirs` are gone.
3. **The panel opens from the card.** On the Components page an installed
   component with the `skills-provider` capability has "Open" (desktop app),
   which opens its skills panel; the panel opens once by itself right after
   the component is added (unless it needs setup first, when its settings
   open instead). Settings → Skills is removed. The web chooses by the
   capability, never a name.
4. **AI**, on the assist seam (built-in `bnbloc.assist.package/v1`
   packages, `runJSONAssist`; no new model client, no agent tool):
   - `review-skill`: the publish step of a local skill ("Add to my library",
     "Publish changes" now open a step instead of publishing at once) shows
     the files and the library entry (name, display name, description,
     tags); "Review with AI" sends the folder's name, its file list and
     SKILL.md (never another file's content) and answers `findings` (a
     secret or personal path left in, an unclear trigger, a missing step)
     and a proposed entry, each field with "Apply". The import route takes
     the entry beside the archive (`?displayName=`, `?description=`,
     `?tag=`; `withImportMeta` re-packs, so the content validator still
     owns the rules). Publishing stays the member's.
   - `fill-settings`: "Fill from a note" on the one settings form (every
     scope): the person pastes a note; the declaration of the form's fields
     and the note go as the text; the answer (`values` items of `key` and
     `value`, `notes`) is kept only for the form's own keys and values the
     field can hold, a secret shown never, and the person ticks what goes
     into the form, then saves (the same checks as a typed value).
   - `suggest-skill-dirs` (proposing extra skill folders from the folders
     the component found and the member's home's top-level tool folders):
     **not built**. The desktop does not list the home's top-level folders,
     and without that input a proposal is a guess about folders the model
     cannot see. The way to build it: the component (it runs as the member
     with the home) reports candidate tool folders (names only) in its
     state, and the panel asks the assist with them.

## 2. Design review

| Lens | Answer |
|---|---|
| Actors | A member (their own list settings, their skills panel, their publish), an org admin (the org's secret rules and skill limit, fill from a note), the super admin (fill on platform settings). The panel and the member's values stay on the member's computer; org values reach members' computers as every process component's do (the form says so). |
| Time and lifecycle | A changed setting reaches local skills at its next policy load (it reads the file on every call) and the desktop restarts a plugin on a member save; an org change restarts it when the catalog says so. The panel auto-opens once, right after an install; while the plugin is still being downloaded it asks again every 3 s (visible-poll). |
| Scale and cost | A list is bounded (64 × 512 bytes); a page message bounds a list at 256. The review sends SKILL.md and at most 200 file names, cut to the assist text bound; fill sends the form's fields and the note, cut likewise. One assist call per click, on the member's budget and rate. |
| Failure and recovery | A refused list names the line ("line 3 is longer than 512 bytes", "has an empty line"). A settings file that cannot be read stops local skills with an error, never ignored; a pattern that does not compile is an error naming it; a folder outside the home is left out and the panel's notes say so. A failed pack or review is shown in the step; the publish is not tried. |
| Boundaries and privacy | A secret proposed by fill is never shown (only "a value from the note"), never logged (the assist audit holds no input); it goes into the masked input. The review never sends another file's content. The panel talks only to the plugin on loopback; the import is the member's own. |
| Abuse and untrusted input | A pasted note and a SKILL.md are untrusted: they travel inside `<text>` (fenced: a golden pins that a note cannot close its block), the instructions say the blocks are data, and the answers are filtered to the form's own keys and the library's rules. Org regexes run in Go's linear-time engine. |
| One owner | The line rule: `pkgmeta.checkSettingLines` (desktop copy and web helper pinned to one fixture); the file shape: `pkgmeta.SettingsFile` (desktop copy pinned); the capability word: `SKILLS_PROVIDER` in `lib/localSkills.ts`; the policy merge: local skills' `Policy.Merge`; the entry rules: the content validator (re-pack). |
| Defaults and fallbacks | An empty list is no value (the component's own lists apply); a list setting's default is a newline-joined string, shown as the textarea's placeholder. Without an assist model the publish step and the form work without their AI buttons. |
| External contracts | The settings file is the platform's contract, now with arrays; the one component reading a list (local skills) reads only arrays and refuses another shape. The other process components declare no list, so their string reads are unchanged. |
| Load on users and agents | One place for a skills component (its card: Open, Settings); no policy file to write. Nothing reaches agents. |
| Release and compatibility | Pre-launch: no fallback for `policyFile`/`agentDirs`, nor for a list read as a string. A desktop older than this release writes a list as a string: local skills 0.2.0 needs a desktop and a platform with `lines` (the platform refuses its manifest before). |
| Observability | Every save is audited as before (keys only); the assist runs are audited (`review-skill@1.0.0`, `fill-settings@1.0.0`). Local skills' notes say what a setting left out. |
| AI capability | review-skill (publish step), fill-settings (every settings form); suggest-skill-dirs not built (above). AI proposes; the person applies field by field and decides. |
| Component or platform | The platform adds a generic type (no component's name), a generic form control, a generic assist on the generic form, and an import parameter any import may use. What a list means is the component's; the panel is the capability's, chosen by the capability. |

**Pre-mortem.**
1. *A member's 65th folder is silently dropped.* The bound is said before
   sending (the form) and refused naming the line (the platform); the
   component never truncates.
2. *An organization's bad regex takes skills away from everyone.* The
   component stops with an error naming the pattern, shown in the panel,
   rather than running without the rule; the admin fixes it in the form.
3. *A secret from a pasted note leaks.* It is shown nowhere, goes only into
   a masked field, and is saved only when the person presses Save; the run's
   audit holds no input.

## 3. Built

Platform (this repository): the `lines` type (wire, `pkgmeta`, the bridge's
settings file, the desktop's copy, the form, the page protocol and host),
the review-skill and fill-settings assists, the import entry parameters, the
skills panel on the card and the publish step, docs. Component: settings in
`package.json` (0.2.0), `LoadPolicy(settingsFile, home)` over
`ReadComponentSettings` and `ComponentSettings.Overlay`, tests, README.
