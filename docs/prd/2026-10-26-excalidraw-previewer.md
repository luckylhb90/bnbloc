# Excalidraw previewer: view, AI edits and manual edits of `.excalidraw` files (2026-10-26)

Status: implemented locally (2026-10-26). Not committed, not pushed, not run
in a deployed frame. Component code: `luckylhb90/bnbloc-previewers`
(`excalidraw/`). Platform part: the `edit` capability and the save messages
in this repository. Verification: section "Verification" below. Source project: [excalidraw/excalidraw](https://github.com/excalidraw/excalidraw)
(MIT, v0.18.0).

## Why

An Excalidraw file is a diagram a member or an agent draws. Today a room
shows it as an unreadable JSON blob (`.excalidraw` has no previewer), and a
change to it means going back to the chat. The AI-first ask: the diagram is
where a member asks the agent to change something ("add a cache between the
API and the DB", "make the arrows from the queue dashed"), sees the proposed
version beside the old one, and decides.

The platform already has the pipeline for that. `docs/prd/2026-10-14-ai-document-editing.md`
puts an Ask AI panel beside the file, sends the selection as fenced data, has
the agent edit the file in its workspace, and publishes the result as the next
version (compare-and-swap on the current version, `editedFrom` on a conflict,
"Restore as new version" as undo). This PRD makes an Excalidraw file a first
citizen of that pipeline, and adds the one thing the pipeline lacks: a member
editing the diagram directly in the frame and saving it as a version.

## What

1. **Read.** Every element is drawn by Excalidraw's own renderer (hand-drawn
   look, its fonts, bound text and arrows). The file's `appState` (theme,
   background) is respected, the view fits the content, and a thumbnail is the
   first render.
2. **AI first (the entry point is Ask AI).**
   - The selection (`preview.selection`) is what the member picked on the
     canvas: each selected element's kind, text, bound label and the labels of
     the elements its arrows connect. The Ask AI chip shows it; the agent gets
     the detail fenced as the file's data (2,000 characters max).
   - The agent changes the file through the existing edit flow. A content
     skill pack (`excalidraw/skill/`, published as content) tells it the scene
     format so the result opens: unique ids, bindings that match, no invented
     fields, unchanged elements left byte-identical.
   - Compare (capability `units`): each element with text is a unit keyed by
     its id, so "what did the AI change" is an element-by-element diff, not a
     wall of JSON.
   - Suggestions come from a prompt pack for this file type (content; it is
     published with the previewer, not built in).
3. **Edit (manual).** The frame runs Excalidraw's editor. Dirty state is shown,
   Ctrl/Cmd+S and the Save button publish the scene as the next version of the
   same artifact. A conflict ("changed meanwhile") is refused, never merged,
   and both versions remain.

Without an assist model or with the Ask AI panel hidden, read, Compare and
manual edit still work: no part of the feature calls a model directly.

## Placement

| Part | Where | Why |
|---|---|---|
| Rendering, the editor UI, selection summary, units, thumbnail, the Excalidraw dependency | **Component** (`bnbloc-previewers/excalidraw`) | Its own runtime and viewer; a second viewer could be installed beside it (Replace test). |
| Know-how for an agent editing a scene (the format, bindings, ids, what to leave alone) | **Content** (`excalidraw/content/excalidraw-skills`, a skill pack) | Guidance and a workflow, not a mechanism; an organization can publish its own. |
| Suggestions for an `.excalidraw` file | **Content** (`excalidraw/content/excalidraw-prompts`, a prompt pack scoped by `files`) | Prompt text belongs to content (`docs/architecture/content-components.md`). |
| Ask AI panel, fenced selection, agent edit, version publish, `editedFrom`, Restore | **Platform** (existing, `2026-10-14-ai-document-editing.md`) | Already built; the reasons are consent and the audit of a shared file. |
| The `edit` capability, `preview.save` / `preview.saved` / `preview.save.failed` | **Platform** (contract, this PRD) | A contract between host and frame; the vocabulary is code-owned (`core/pkgmeta`, `web/previewer-sdk`) and checked by a test. The save itself is the existing `publishArtifactFile` with a compare-and-swap, not a new publish path. |

## Contract change: capability `edit`

Added to the frame protocol (`web/previewer-sdk/src/protocol.ts`) and the
package validator (`core/pkgmeta/previewer_validation.go`), kept equal by the
existing vocabulary test:

- Capability `edit` (the reserved word in the vocabulary is now a capability).
  A previewer that declares it may send `preview.save`. No other rule: a
  previewer without it cannot save, and its frame does not offer saving.
- Frame → host: `preview.save {bytes: ArrayBuffer, summary?: string}`.
  `summary` ≤ 200 characters, the version's summary line. `bytes` at most the
  room's blob-version cap (the cap the existing file publish enforces; the
  frame checks it too).
- Host → frame: `preview.saved {version: number}` on success;
  `preview.save.failed {message: string}` (≤ 500 characters) otherwise.

Host behaviour (`web/src/views/artifact/RemotePreviewer.tsx`):

- Honoured only when the descriptor has `edit`, and only for a room artifact
  (`roomId` and `artifactId` present). Otherwise: `save.failed` with "This file
  cannot be saved here."
- The base version is the version the frame was handed in `preview.init`
  (`loadedVersion`), not the newest one the workbench shows. It is updated on
  success to the version the save produced, so a second save of the same frame
  does not conflict with the first.
- Publish: `api.publishArtifactFile(roomId, artifactId, {file: Blob(bytes,
  mimeType), fileName, baseVersion, summary})`. A conflict (409,
  `ErrArtifactVersionConflict`) is `save.failed` with "changed meanwhile: vN is
  newer; reload the file to take it". Nothing is merged.
- The workbench's version list and "Updated · vN" follow the existing path; the
  frame keeps its scene (it already has the bytes it saved).

Limits the frame cannot exceed: `bytes` ≤ the room version cap, and the frame
shows "Saving did not answer" after 10 s without `saved` or `save.failed`
(an older host ignores unknown messages).

## Design review

1. **Actors.** Every member who can see the file can edit it and ask the
   agent; the last save wins only if it was based on the current version,
   otherwise it is refused. Two agents or an agent and a member: the same
   compare-and-swap.
2. **Time and lifecycle.** A frame loaded on v3 that saves after v4 arrived is
   refused (conflict), not overwritten. A deleted artifact: the save fails with
   the publish's error. The frame keeps unsaved edits while it is open; a
   reload discards them (the Save button says so when dirty).
3. **Scale and cost.** A large scene (thousands of elements) is rendered by
   the browser's canvas, not the server. The saved file is one blob version
   under the room cap. Units are capped (5,000 per file, `UNITS_LIMIT`).
4. **Failure and recovery.** Save fails: the scene stays in the frame, the
   error stays visible, Save can be retried. The bundle fails to load: the
   existing "Loading the previewer" notice and its error path.
5. **Boundaries and privacy.** The frame has no network (`connect-src 'none'`),
   no storage, no navigation. The file is not sent anywhere but the room's
   version store and the member's own agent (through Ask AI). Embedded images
   (`dataURL` in `files`) are data, rendered as `img-src data:`.
6. **Abuse and untrusted input.** An Excalidraw file is untrusted: the reader
   validates the envelope (`type: "excalidraw"`, `elements` an array, element
   count capped), never executes a script, and links (`link` on an element)
   are displayed as text, not followed. Selection text and units are data,
   fenced for the agent.
7. **One rule, one owner.** The save goes through the existing
   `publishArtifactFile` and its CAS; the vocabulary lives in the SDK protocol
   and is mirrored by the Go validator under a test; the scene format is read
   by one module (`excalidraw/src/model.ts`) for the view, the units, the
   selection and the save check.
8. **Defaults and fallbacks.** A file without `appState` opens with defaults.
   A file that is not an Excalidraw scene is refused with a sentence. An older
   host: no `edit` in the catalog → no Save; a save to an older host times out
   with a notice.
9. **External contracts.** New capability and two host messages; a previewer
   that predates them keeps working (it never sends `save`). The package
   declares `edit`, so an older gateway refuses the package at install (strict
   vocabulary), which is the safe failure.
10. **Load on users and agents.** The member clicks, types and saves. The agent
    gets the existing edit line and the selection; the skill pack is loaded only
    when an expert or room uses it (content, not a rule for every turn).
11. **Release and compatibility.** Order: platform (protocol, validator, host)
    first; then the previewer package with `--rollout optin` (it declares
    `edit`, and an older platform refuses it).
12. **Observability.** The publish path is the existing one (its audit row, the
    version's summary). Missing: a metric for `preview.save` outcomes. Follow-up.
13. **AI capability.** The AI entry point is Ask AI on the selection and the
    whole file (existing), with the skill pack and suggestions as content. A
    one-click "Draft with AI" inside the frame (an assist task returning a scene
    that the member accepts) is the next step; it would need a new assist task
    and an output contract. Not in this change, and not replaced by any other
    model call here.
14. **Content, component or platform.** See the placement table.
15. **Pre-mortem.** It shipped and a month later it failed badly:
    - *"My diagram got replaced by an old version."* Impossible by design: the
      base is the loaded version and a conflict is refused.
    - *"The AI and I both edited and one change vanished."* A conflict is
      refused, both versions exist, Compare shows the element diff.
    - *"Excalidraw's fonts or workers were blocked in the frame and the diagram
      looked wrong."* The renderer falls back to bundled fonts; the frame is
      tested with its real CSP (see Verification, not run yet).

## Build decisions (component)

- `@excalidraw/excalidraw` 0.18.0 (React embed), esbuild iife bundle through
  `lib/bundle.mjs`. Mermaid-to-excalidraw (the Mermaid import) is stubbed: this
  viewer never converts Mermaid, and the stub keeps the bundle to about 6 MB.
- Fonts: the families Excalidraw uses by default are bundled
  (`Excalifont`, `Virgil`, `Cascadia`, `Assistant`, `Liberation`, `Lilita`,
  `Nunito`, `ComicShanns`, about 1 MB). `Xiaolai` (Chinese hand-drawn, 12 MB) is
  not bundled: Chinese text falls back to the system font. Recorded as a known
  gap.
- Fonts and the stylesheet are served beside `main.js` (the frame base). The
  fonts' fallback URL in Excalidraw is its CDN (esm.sh), which the frame
  refuses; a font whose bytes cannot be fetched falls back to the last URL in
  its list, so the build rewrites that one fallback to the frame's own
  directory (`build.mjs`, `ownFontBase`, count checked). Found in the browser
  check, not by reading the code.
- The frame has no `fetch` and no `XMLHttpRequest` (the build makes both
  undefined, as drawio does for its loader). Excalidraw's AI features are off
  (`aiEnabled={false}`), and the Library button stays: it opens with no storage
  and raised no error in the check.
- Licences: the npm packages of Excalidraw and the Radix primitives ship no
  licence file. `lib/notices.mjs` takes a fallback directory per package; the
  upstream MIT texts are vendored under `excalidraw/licenses/` (fetched from
  their repositories). Without them the MIT packages would be bundled with no
  text, which MIT does not allow.
- The SDK (`@bnbloc/previewer-sdk`) must be the version with the `edit`
  messages. The root pin moves after this change is pushed to the public repo.

## Verification

Run on 2026-10-26, on the working tree (uncommitted):

- Platform: `go test -count=1 ./core/pkgmeta/...` ok; web `node --test` over
  `src/**/*.test.ts`, `scripts/*.test.mjs` and `previewer-sdk/src/*.test.ts`:
  205 pass, 0 fail; `tsc` for the web project exit 0.
- Component: `pnpm test` in `bnbloc-previewers` (all previewers): 72 pass, 0
  fail, including `excalidraw/build.test.mjs` (10: a deterministic archive, the
  bundle checks, notices, fonts without the Chinese one, the reader's refusals,
  the selection's wording and ids, units keyed by id, moves are not content
  changes, the content packages). `pnpm run typecheck` exit 0.
- Platform's content checker: `go run ./cmd/bnbloc content check` on both
  content packages: ok.
- Browser, in headless Chromium under the frame's CSP (`connect-src 'none'`,
  `font-src` the frame base, no `worker-src`), in a sandboxed opaque-origin
  iframe that speaks the protocol: `excalidraw/check/frame-check.mjs`, 17 of 17
  checks: render with bundled fonts, a clean load has Save disabled, toolbar,
  thumbnail, selecting a box names it with its id and connection, units, an
  edit enables Save and sends the edited scene as `preview.save`, `saved`
  shows "Saved as v2", a refused save keeps its message and stays editable,
  reveal selects the element, a non-scene is refused, the Library button does
  not break the editor. No console errors or page errors. The check runs with
  site isolation off (headless Chromium drops pointer events in an out-of-process
  iframe below the top of the frame; a test-browser artifact).

Not run (say so in any report): a real frame behind the gateway (the bundle is
served by the gateway's artifact route and the desktop scheme, not by the
check's server), the desktop's `bnbloc-previewer` scheme, a save against a
live room with two members (the conflict is shown with a stubbed host answer,
not from the room), the agent's edit of a file through Ask AI (the skill pack
was not exercised by an agent), and a QA smoke. These are the before-release
checks.

Known gaps: Chinese hand-drawn text falls back to the system font (Xiaolai is
not bundled); the Mermaid conversion is stubbed; the SDK pin of
`bnbloc-previewers` must move to the commit that carries `savePreview` before
the component builds in CI (locally the root `node_modules` link points at the
working tree).

## Open

- Draft with AI inside the frame (lens 13).
- Metrics for save outcomes (lens 12).
- A prompt pack for `.excalidraw` suggestions (content, its own repository).
- Chinese hand-drawn font (`Xiaolai`, 12 MB) when a size budget exists.
