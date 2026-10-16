# Ask AI for every kind of file: pointing and suggestions as parts (2026-10-16)

Status: proposed (2026-10-16). Builds on
`docs/prd/2026-10-14-select-edit-review.md` (selections, kinds, the floating
action, selection suggestions as prompt-pack prompts),
`docs/prd/2026-10-14-file-prompts-and-compare.md` (file prompts) and
`docs/prd/2026-10-16-diagram-previewers.md`.

## 1. Why

Pointing at a part of a file and asking for a change works for decks,
documents, sheets, images, audio and video, and for the diagrams. It does not
for the files members open most:

- **Six built-in viewers cannot point.** Only the image viewer (region), the
  media viewer (time range) and installed previewers report a selection.
  Markdown, code, plain text, JSON, CSV tables and PDF report none, so a
  member who selects a paragraph, three lines of code, a JSON value or a
  table range gets no action beside it and no chip in the panel: they type
  where it is.
- **The suggestions are text suggestions everywhere.** A selection in a diagram
  offers "Make it shorter" and "Fix the wording"; a selection in code offers
  "Rewrite this". The prompts are data (prompt packs scoped by selection kind
  and by file), but the built-in pack covers decks, documents, sheets, PDFs,
  Markdown, tables, images and media only, and a prompt for any file shows
  beside the ones for this kind of file.

## 2. Rules

1. **A file type is two independent parts, and the host names neither.**
   - *How to point*: the viewer (built-in or a previewer component) reports a
     selection through the one pointing engine (rule 2).
   - *What to suggest*: prompt packs, matched by the file (extension, MIME
     type) and by the selection's kind. No host code branches on a type.
   Adding a type is adding a viewer's part rules and a pack, not editing the
   host.
2. **One pointing engine.** `web/previewer-sdk/src/pointing.ts` owns
   "text the member selects, or the part they click, becomes a selection": the
   click-versus-drag rule, toggle and Escape, the chip label, the detail
   (bounded), the `rect` re-reported while the view scrolls. It takes the
   report as a function, so a previewer frame (`sendSelection`) and a built-in
   viewer (`onSelection`) use the same code. A viewer supplies **part rules**
   as data (a selector, a noun, a kind, optionally a detail and keys) and, for
   text, how to say where a range is (a line range, a JSON path, a page).
   `examples/previewers/lib/selection.ts` keeps its API and delegates.
3. **The built-in viewers point.**

   | Viewer | Text selection says | A click picks | Kind |
   |---|---|---|---|
   | Markdown | the block it starts in | heading, paragraph, list item, quote, code block, table | `text`, `block` |
   | Code, plain text | `Lines 12–18` | a line (its number) | `text`, `block` |
   | JSON | the path of the value it starts in | a value or an object, by path | `text`, `block` |
   | CSV, TSV | — | a cell, a range (shift-click), a column (its header) | `cells` |
   | PDF | `Page 3` | — | `text` |

   Links, buttons and toggles inside a viewer are not parts. A CSV range
   carries the cell addresses as `keys` (at most 500, the same addresses
   Compare's units use); a larger one sends none. The detail is bounded as
   everywhere (`wire.MaxFileSelectionDetail`).
4. **The vocabulary does not change.** The six selection kinds stay; a line
   range is a `text` selection whose label and detail say the lines.
5. **Scoped suggestions replace generic ones.** For a selection, the prompts
   scoped to this kind of file *and* the selection's kind are offered; only
   when there are none are the kind's prompts for any file offered (and then
   the file's own, as before). A diagram box never offers "Make it shorter".
6. **Built-in packs per domain, each a component.** `file-prompts` (Office,
   PDF, Markdown, tables, images, media) gains selection prompts for PDF text;
   `code-prompts` covers source files, JSON and configuration; `diagram-prompts`
   covers Mermaid, draw.io and SVG. A pack can be turned off or replaced by an
   admin, and a Library pack adds prompts for any type, including the type of
   a previewer someone else ships, by the same `files` and `selection` fields:
   the previewer's author or an admin publishes the pack (the Library already
   does this); a previewer package carries no prompts of its own.
7. **Not covered: web apps.** An interactive app's version is a site snapshot
   (a bundle, not a file an agent edits); the server refuses a `fileRef` to it
   ("ask in the chat"). That is a different mechanism and stays out.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | A selection is the member's own view state and travels only with their request, as before. Nothing new is visible to others except the request's label and detail. |
| Time and lifecycle | A selection clears when the version shown changes (existing). A virtualized table or a long file re-reports the rect on scroll; a range whose rows scroll out stays valid (it is addresses, not nodes). |
| Scale and cost | Code and CSV stay virtualized: the engine reads the DOM Range and the clicked row only, never the whole file. Detail ≤ the wire cap; CSV `keys` ≤ 500 else none. |
| Failure and recovery | A viewer without part rules still works whole-file. A selection the engine cannot describe is dropped, not guessed. An unknown pack prompt is data the server already validated. |
| Boundaries and privacy | The PDF guest posts only the selected text, its page and its rect, validated and bounded by the host (like every guest message); the rect is layout and is never sent to the agent. |
| Abuse and untrusted input | Selected text is untrusted content bounded and carried as text (the detail is quoted, never markup). The PDF guest's message is read through the existing validated reader. |
| One owner | Pointing: the SDK engine. Suggestions: prompt packs. Kinds: the one vocabulary. CSV addresses: the same function Compare uses (`columnName`). |
| Defaults and fallbacks | No part rule: no pointing, whole-file Ask AI as today. No scoped prompt: the generic ones. No pack for a type: the generic ones. |
| External contracts | None on the wire: `fileRef.selection` is unchanged. Previewer SDK gains an export; existing previewers keep working. Packs are `bnbloc.prompt.package/v1` as now. |
| Load on users and agents | Fewer keystrokes; the agent gets "Lines 12–18 of main.go" or "Page 3" instead of nothing. No new tools or rules. |
| Release and compatibility | Web and server independent: new packs appear on a server deploy (older web shows them as ordinary prompts); the web change works without them (generic prompts). Previewers need no new version. |
| Observability | None new: the chip names what was picked. |

**Pre-mortem.**

1. **"I selected text and the chip says nothing useful."** A range in a code
   block that crosses the line gutter. Gutter numbers are `user-select: none`;
   the line range comes from the rows the range starts and ends in.
2. **"Clicking a link in a Markdown file asks the AI instead of opening it."**
   Links, buttons, inputs and summaries are never parts, and a press that
   moves is a drag, not a click.
3. **"The action bar floats in the wrong place."** Built-in viewers report
   rects in the tab's viewer box (not the page), re-reported on scroll and
   resize; the host hides it while off-screen (existing).
4. **"A pack I did not write suppresses the generic prompts."** Only a prompt
   scoped to both the file and the selection kind does; the rule is stated in
   one function with tests.

## 4. Milestones

- **A1:** SDK pointing engine, previewers delegate (no behaviour change), tests.
- **A2:** built-in viewers point: Markdown, code and plain text, JSON, CSV, PDF.
- **A3:** scoped-first suggestions; `code-prompts`, `diagram-prompts`, PDF
  selection prompts.
- **A4:** verify on QA against real files of every type; docs.
