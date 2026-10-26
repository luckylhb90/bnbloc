# Excalidraw: draft a diagram with AI, inside the editor (2026-10-26)

Status: proposed (2026-10-26). Builds on
`docs/prd/2026-10-26-excalidraw-previewer.md` (the previewer, the `edit`
capability). Component: `luckylhb90/bnbloc-previewers` (`excalidraw/`).

## Why

A member who opens an empty or half-drawn Excalidraw file wants to say "a
login flow with a password reset branch" and see boxes and arrows appear.
Today the only AI path is Ask AI on a selection, which asks an agent turn to
edit a file: slow to start, and it needs a bridge online. A diagram from one
sentence is a single fast call to the deployment's assist model, the same seam
every manual entry point uses (`docs/architecture/ai-assist.md`).

## What

1. **Entry.** A "Draft with AI" box in the frame's footer: a text field and a
   Draft button. Shown in every drawing.
2. **Draft.** The frame asks the host for a draft of the text. The host runs
   the assist task the previewer declares, and returns the task's reply. The
   reply is a list of boxes (`id`, `label`) and arrows (`from`, `to`, optional
   `label`), plus a title.
3. **Placement.** The frame lays the draft out left to right by arrow depth
   (`excalidraw/src/model.ts`, `layoutDraft`) and adds it to the drawing, to
   the right of what is already there (at the origin when the drawing is
   empty). It is one undoable step. "Remove draft" takes out exactly the
   elements the draft added (other edits stay); Ctrl/⌘ Z also undoes it.
4. **Decide.** The draft makes the file unsaved. Nothing is shared until the
   member saves (`edit`, Ctrl/⌘ S), which makes it the next version. Compare
   shows it element by element.
5. **Failure.** No assist model, the organization's assists off, the task not
   published, a reply the frame cannot read: the footer says why, and the
   editor keeps working.

The prompt is the member's text. The existing drawing is not sent to the
model in this version.

## Placement

| Part | Where | Why |
|---|---|---|
| The prompt, the diagram's schema (boxes, arrows, labels, limits), what makes a good diagram | **Content**: assist package `draw-diagram` (`excalidraw/content/excalidraw-assist`) | Guidance and an output contract; an organization can publish its own. |
| The layout, the undo step, "unsaved until Save", the footer | **Component** (`excalidraw/`) | Its own viewer behaviour; a second editor could be installed beside it. |
| The assist seam (the model, the budget, the audit, the org off switch) | **Platform** (existing `/v1/assist/{task}`) | Already built; consent and the budget are platform reasons. |
| Naming the draft task per previewer (`previewer.draft.task`) and the frame messages | **Platform** (contract) | The host must not name a previewer's task: it runs the one the package declares, and only that one. |

The host holds no diagram knowledge. It passes the task's reply to the frame
after a size bound; the frame validates it against the diagram schema.

## Contract

Previewer manifest (`previewer.json`), strict-parsed:

- capability `draft` (new in the vocabulary, next to `edit`);
- `draft: {"task": "<content name>"}`, required with `draft`, and refused
  without it. The name follows the content name rule (`ValidName`).

Installed catalog entry: `draftTask` (the task name), present only when the
package declares `draft`.

Frame to host: `preview.draft {prompt}`. `prompt` is 1 to 1000 characters.

Host to frame:

- `preview.drafted {reply}`: `reply` is the task's JSON fields, at most 64 KiB
  serialized. The host checks only that it is a JSON object of that size.
- `preview.draft.failed {message}`: at most 500 characters.

Host behaviour (`web/src/views/artifact/RemotePreviewer.tsx`):

- Refused with a sentence when the descriptor has no `draft` capability or no
  `draftTask`.
- Otherwise runs the task through the assist client
  (`web/src/lib/assist.ts`, the one owner of the assist route): `runJSONAssist`
  with `{text: prompt}`. An error (no model, assists off, task not found)
  becomes `preview.draft.failed` with the server's reason.
- No room is needed: a draft is the member's own request.

## Design review

1. **Actors.** The member who asks; the org that set its model and budget.
   Two members drafting into one file: each draft is an unsaved local step;
   the save is the usual compare-and-swap.
2. **Time and lifecycle.** A slow call shows "Drafting…" and can be left; the
   result is applied only to the frame that asked. A file closed mid-draft: the
   reply is dropped.
3. **Scale and cost.** Bounded reply (64 KiB), bounded diagram (40 boxes, 80
   arrows, labels 120 characters). The member's assist budget and rate apply,
   as for every assist.
4. **Failure and recovery.** A reply the frame cannot read is refused with a
   sentence; the member edits the prompt and drafts again. Nothing is added
   on failure.
5. **Boundaries and privacy.** Only the prompt leaves the frame, through the
   host. The drawing does not. The assist's model sees the member's text only.
6. **Abuse and untrusted input.** The model's reply is untrusted: labels are
   drawn as text (never HTML), counts and sizes are bounded, unknown arrow ends
   are refused. A prompt that asks for something else makes an odd diagram; the
   member sees it and can undo it.
7. **One rule, one owner.** Assist requests stay in `web/src/lib/assist.ts`.
   The diagram schema is read in one place (`excalidraw/src/model.ts`).
8. **Defaults and fallbacks.** No draft task declared: no footer box. Assists
   off: the footer says so.
9. **External contracts.** New capability, one manifest block, one catalog
   field, two frame messages. An older host refuses the package at install
   (strict vocabulary), so the previewer ships with the platform change.
10. **Load on users and agents.** No agent turn. The member types one sentence.
11. **Release and compatibility.** Platform first (the vocabulary, the catalog
    field, the host); then the previewer as version 1.1.0 with `--rollout
    optin`; then the assist package (`bnbloc content publish`).
12. **Observability.** The assist's usage and audit are the platform's. Missing:
    a count of drafts that were kept or undone. Follow-up.
13. **AI capability.** This feature is the AI entry point: one sentence, a
    draft, a person decides (keep with Save, or undo). Without a model the
    editor works and says why the draft is gone.
14. **Content, component or platform.** See the placement table.

**Pre-mortem.** It shipped and a month later it failed badly:

- *"It replaced my diagram."* It adds to the drawing, is one undo step, and
  is unsaved until Save.
- *"It drew a tangle nobody can read."* Layered layout, limits on size. The
  member asks again with a narrower prompt.
- *"The button did nothing."* Every refusal is a sentence in the footer.

## Verification (to run)

- `excalidraw/build.test.mjs`: `layoutDraft` (depth order, placement right of
  existing boxes, labels, limits), the reply reader (unknown arrow ends
  refused, count limits), the manifest's `draft` block.
- Platform: protocol tests for `preview.draft` (bounds) and
  `preview.drafted` / `preview.draft.failed` (the size bound); pkgmeta tests
  for the `draft` block and the capability; RemotePreviewer refusals (no
  capability, no task).
- Browser check (`excalidraw/check/frame-check.mjs`): the host answers
  `preview.draft` with a canned reply; the boxes and arrows appear; Save is
  enabled; "Remove draft" leaves the seven original elements; a failed draft
  shows its sentence.

Ran on 2026-10-26 against the local SDK (the working tree), not the pinned
one: `excalidraw/build.test.mjs` 16 of 16, `pnpm run typecheck`, the browser
check 23 of 23, no page or console errors. Found by the browser check, and
fixed: the frame's sandbox allows scripts but not forms, so a draft is a button
click or an Enter key, never a form submit.

Not run yet: a real assist model answering the `draw-diagram` task; the
platform change is not pushed, so the component cannot pin it yet.

## Open

- Revise on a bad reply (the host's checked-JSON loop) would need the frame's
  rules on the host: not in this version.
- Sending the drawing to the model (so "add a database to this" works): a
  later version, with its own consent note.
