# Select, edit, review: the loop around one change (2026-10-14)

Status: G1–G5 implemented (2026-10-14), not yet released. Builds on
`docs/prd/2026-10-14-ai-document-editing.md` (the Ask AI panel, M2's
selection) and `docs/prd/2026-10-14-file-prompts-and-compare.md`
(file prompts, units, Compare).

## 1. Why

A member can already point at a part of a file and ask for a change, but:

- **They still type.** A selection offers no ready actions; the panel's
  suggestions are about the whole file.
- **They cannot see what the edit changed.** That takes opening Compare and
  picking versions by hand.
- **Nothing says whether the edit stayed in the selection.** "Fix slide 2"
  that also rewrote slide 4 passes unnoticed.
- **Images and video have no selection at all**, and their versions
  compare only as two images, or not at all.

## 2. Rules

1. **A selection says what it is, where it is, and what it covers.** The
   frame protocol's Selection gains optional fields:
   - `kind`: one of the code-owned `SELECTION_KINDS`: `text`, `block`,
     `slide`, `cells`, `region`, `time`;
   - `rect`: where it shows, in the viewer's box, CSS pixels;
   - `keys`: the unit keys it covers, at most 500 (Compare's units: a
     slide's key, every cell address of a range).

   Built-in viewers report the same shape. The chat `fileRef.selection`
   carries `kind` and `keys`, validated in realtime against the same
   vocabulary (`wire.FileSelectionKinds`, kept equal to the SDK's by the
   vocabulary test).
2. **Selection suggestions are prompt-pack prompts scoped to a selection
   kind.**
   - A prompt may declare `selection` (kinds); validated against the same
     vocabulary.
   - A starter with `selection` is offered when the current selection is of
     one of those kinds (and its `files`, if any, match the file).
   - Without a selection, file suggestions show as today. With one, its
     suggestions show, falling back to the file's when none apply.
   - Selection prompts are never room starters.
   - The built-in `file-prompts` pack ships them as data: rewrite, shorten,
     expand or fix a text; tighten or redesign a slide; explain, total,
     chart or check cells; describe, remove, enhance or crop an image
     region; cut, describe or caption a time range.
3. **A selection has an action beside it.** Where the viewer reports a
   `rect`, the tab shows a small floating action at it: the selection's top
   suggestions and "Ask…". A pick opens the Ask AI panel with the prompt
   inserted (the composer's own prompt flow).
4. **Every edit gets a change card.**
   - In the file's Ask AI thread, after the agent's reply, each version the
     request produced (`sourceSeq` = the request) shows a card:
     - what changed: the changed units' labels, or "a new version" for a
       file compared by view;
     - **View changes**: Compare, those two versions;
     - **Show**: the preview scrolled to the first change (rule 6);
     - **Undo**: the previous version restored as the next one, the same
       publish as the version picker's "Restore".
   - The changes come from the same hook as Compare (one owner), and units
     are cached per artifact, version and comparer, so a card and Compare
     never read a version twice.
5. **The card says when an edit left the selection.**
   - With `keys`, any changed, added or removed unit outside them is
     named: "Also changed outside your selection: Slide 4".
   - Without keys (paragraphs, pages), a changed or removed unit whose
     text does not overlap the selection's text is named.
   - Nothing is undone automatically; the member decides.
6. **Show takes the preview to a change.**
   - A unit may carry `at`, where it is in that version (`slide:2`,
     `Revenue!C3`, `p:7`, `page:2`).
   - The host asks the viewer to reveal it:
     - an installed previewer gets `preview.reveal {at}` (it comes with the
       `units` capability);
     - the PDF viewer goes to the page;
   - Compare's units each get a Show too.
7. **Images and media.**
   - The image viewer gets a "Select area" mode: drag a rectangle (image
     pixels), shown on the image; a `region` selection.
   - The media viewer gets "Mark start" and "Mark end" on the timeline; a
     `time` selection.
   - Image Compare gains a Difference view: changed pixels highlighted, when
     both versions have the same size.
   - Video and audio compare in two players kept in step (play, pause and
     seek together).
   - Built-in file suggestions cover video and audio: summarize, caption
     or transcribe, cut a highlight.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | A selection is the member's own view state; it travels only with their request. The change card shows to everyone who can see the thread; Undo is a normal publish by whoever clicks it, recorded as such. |
| Time and lifecycle | A selection clears when the version shown changes. A card computes its changes when it scrolls into view, from cached units; a version gone from the room shows no card. Undo on a version that is no longer current restores it on top of the current one ("Restore v2"), never rewinds history. |
| Scale and cost | `keys` ≤ 500; a bigger range sends none and falls back to text overlap. Units are cached (an LRU of 24 versions). A card lists at most four changed units, then "and N more". |
| Failure and recovery | A comparer that fails leaves a card with View changes and Undo only. A reveal the viewer cannot place does nothing. A floating action off-screen is not shown. |
| Boundaries and privacy | The rect is layout, not content, and is never sent to the agent. Region and time selections send coordinates and times, not pixels or frames. |
| Abuse and untrusted input | Selection fields from a previewer are bounded and validated by the host as before; `at` is an opaque string ≤ 200 the host only hands back to the same viewer. |
| One owner | Selection kinds: one vocabulary (SDK + wire, tested equal). Suggestions: prompt packs. Changes: one hook for Compare and cards. Restore: one function for the version picker and Undo. |
| Defaults and fallbacks | No kind: file suggestions. No rect: no floating action (the panel still works). No keys: text overlap. No units: "a new version". |
| External contracts | Protocol: Selection `kind`/`rect`/`keys`, Unit `at`, `preview.reveal`. Wire: FileSelection `kind`/`keys`. Prompt packs: `selection`. Previewers 1.3.0 send them; older ones keep working without the extras. |
| Load on users and agents | Fewer keystrokes; the agent gets the same selection line, the kind stated in its label. No new tools. |
| Release and compatibility | Server first (wire, packs), then web and desktop, then previewers 1.3.0 optin → auto. An older host ignores the new selection fields and `preview.reveal` is only sent to previewers that declare `units`. |
| Observability | Undo is a version with its summary ("Undid v3"); outside-selection warnings are visible in the thread. |

**Pre-mortem.**

1. **"The card cries wolf."** A sheet edit inside the selected range fills a
   cell that was empty, so its key was not in the selection. Ranges send
   every cell address in the range (empty ones included), up to 500.
   Paragraph overlap ignores insertions next to the selection.
2. **"Undo lost my other edit."** Two edits landed after the one undone.
   Undo is a restore on top of the current version, and the card says
   which version it restores ("Restore v2 as v5"), so nothing later is
   lost.
3. **"The floating button covers what I selected."** It sits below the
   selection's rect, flips above when there is no room, and hides while
   dragging.

## 3b. The popped-out window

A previewer torn out into its own window (`#/preview/<room>/<artifact>`)
has no room view, so it had no Ask AI. It now loads what the panel needs
with the room's own hooks (members, agents, the default channel's feed
over its one event stream) and builds the panel's composer with the same
hook as the room (`useRoomFileAsk`). Requests go to the room's default
channel. The window reads no cursor, so opening a file there marks
nothing read. A run-limit refusal says so in a toast (its request dialog
lives in the room window).

## 4. Milestones

- **G1:** selection fields (protocol, wire, realtime), prompt `selection`,
  built-in selection prompts, selection suggestions in the panel.
- **G2:** the change card, Undo, the changes hook and units cache, the
  outside-selection note.
- **G3:** `at` and `preview.reveal` in the Office previewers, the PDF
  viewer's page reveal, Show in cards and Compare.
- **G4:** the floating selection action, the Office previewers sending
  `kind`/`rect`/`keys` (1.3.0).
- **G5:** image region selection and Difference, media time selection and
  synced media Compare, video and audio suggestions.
