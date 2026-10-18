# Diagram previewers: Mermaid and draw.io files, and pointing at a diagram (2026-10-16)

Status: proposed (2026-10-16). Builds on
`docs/architecture/previewer-components.md` (previewers are Components),
`docs/prd/2026-10-14-select-edit-review.md` (selections, `region`, units,
Compare) and `docs/prd/2026-10-14-file-prompts-and-compare.md`. Since 2026-10-18 the component is its own repository, `luckylhb90/bnbloc-previewers` (`docs/prd/2026-10-18-components-in-own-repos.md`).

## 1. Why

A member asked an agent for a flowchart "I can edit". The agent drew an
SVG and a Mermaid file and said it had not been able to test the Mermaid
file: **the room could not show it**. A `.mmd` or `.drawio` file opens as
text. The member then pointed at the picture, and the agent received only a
rectangle of pixels ("region 120,300 → 260,360"), which names nothing in a
diagram, so it guessed what to change and changed something nobody asked
for.

Two gaps:

- **Diagram sources have no viewer.** Agents write diagrams as text
  (Mermaid) or XML (draw.io, which members can also edit in diagrams.net).
  Only Markdown code blocks render Mermaid, and nothing renders `.drawio`.
- **Pointing at a drawing names no part.** For a diagram the parts are its
  boxes and arrows, and their labels are what the request is about.

## 2. Rules

1. **Two previewer Components, not built-ins.** `mermaid` (`.mmd`,
   `.mermaid`) and `drawio` (`.drawio`, `.dio`) are installable previewers
   like `pptx`: signed, rolled out `optin` first, no network, bundled
   libraries (`mermaid`; `@maxgraph/core`, the maintained successor of the
   library draw.io itself is built on), attributions in `NOTICES.txt`.
2. **Untrusted sources stay inert.**
   - Mermaid renders with `securityLevel: "strict"` (labels are encoded,
     clicks and scripts disabled).
   - A draw.io label is sanitized HTML (DOMPurify, a short allow-list of
     inline formatting); links are not followed (`keepLinksInside`).
   - Embedded images load only as `data:` URIs (the frame's CSP allows no
     other).
3. **A syntax error says where.** A Mermaid file that does not parse shows
   its error and line over the frame (`preview.error`), and keeps the last
   good drawing out of the way: a stale picture of a broken file is worse
   than none.
4. **Pointing names the part.** Both previewers declare `selection`.
   - Clicking a box or an arrow reports `kind: "block"`, the label as the
     chip (`Box “P2P check passed?”`), and as detail what the agent needs to
     act on it: for Mermaid the node's text and the source lines naming it;
     for draw.io the cell's id, text, shape and the cells it connects.
   - draw.io cells carry their id as the selection `keys`, so the change
     card and Compare name the same unit.
   - A click on the background, or Escape, clears it.
5. **The built-in SVG viewer names parts too.** A region picked on an SVG
   carries the text labels inside it (the detail ends `Labels in it: “Start”,
   “Check passed?”.`), found in the SVG's own `<text>` elements through its
   `viewBox` (`web/src/lib/svgLabels.ts`); any other image keeps the plain
   region. The markup is read, never laid out (the viewer shows an SVG as an
   `<img>`), so a label counts when the middle of its estimated text box is in
   the area; a drawing that positions text with CSS classes, `<use>` or
   `foreignObject` yields fewer labels, never wrong pixels.
6. **Compare works whatever the MIME says.** Both declare `units`:
   Mermaid's units are its source statements (comment and blank lines
   dropped, aligned by text); draw.io's are its cells keyed
   `<page id>/<cell id>` (label, shape, and for an edge its ends), so a
   moved-but-unchanged box is not a change and a relabelled one is. Show
   (`at: "cell:<page>/<id>"`) scrolls to and flashes the cell.
7. **No editor.** Editing stays what it is: ask the agent (the Ask AI panel
   on the file), whose request now names the part. A draw.io file the
   member wants to edit by hand is downloaded and opened in diagrams.net;
   the previewer is for seeing it and pointing at it.

## 3. draw.io, in detail

- **Format.** `.drawio` is `<mxfile><diagram id name>…</diagram>…</mxfile>`
  where each diagram is an `<mxGraphModel>` or the same XML deflated,
  base64-encoded and URL-encoded; a bare `<mxGraphModel>` file is read too.
  Compressed diagrams are inflated in the frame (`DecompressionStream`,
  bounded to 8 MiB inflated); a file with more than 50 pages shows the first
  50 and says so.
- **Rendering.** maxGraph lays out and routes (orthogonal and elbow
  connectors, perimeters, label positions). draw.io writes its default
  shapes as bare style names (`ellipse;`, `rhombus;`) resolved by its own
  stylesheet and uses shape names maxGraph lacks (`cylinder3`,
  `parallelogram`, `process`, `document`, `step`, `cube`, `note`, `folder`,
  `card`, `trapezoid`). A **data table** (`src/shapeTable.ts`) maps bare names
  to styles and names to custom shapes; a shape it does not know is drawn
  as a plain box with its label (never blank, never an error).
  Adding a shape is adding a row.
- **Pages** are tabs under the drawing, like a workbook's sheets; the
  toolbar has zoom and fit.
- **Bounds.** At most 5,000 cells are drawn (the rest are named in a notice),
  the file at most 8 MiB (the previewer limit is the host's).

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | A previewer only renders what the member can already read; its selection travels only with that member's request. The agent is the same agent: nothing new to learn. |
| Time and lifecycle | A file that changes (a new version) re-renders from its bytes; a pending Show waits until the drawing is up. A previewer upgrade retires older frames (existing rule). |
| Scale and cost | Mermaid is parsed once per version; draw.io is capped (cells, pages, inflated bytes). Bundles are ~3 MB (Mermaid) and ~0.6 MB (draw.io), one-time, cached by hash. |
| Failure and recovery | Bad XML, a Mermaid syntax error, a corrupt deflate stream: `preview.error` with the reason; an unknown draw.io shape draws as a box. A failed render never leaves the previous version on screen. |
| Boundaries and privacy | Opaque origin, `connect-src 'none'`; the bundle reads only the bytes the host hands it. |
| Abuse and untrusted input | §2 rule 2; the inflate bound stops a zip-bomb diagram; the cell cap stops a billion-cell file. |
| One owner | Selection kinds and keys: the frame protocol (unchanged vocabulary). Label and rect plumbing: `examples/previewers/lib/selection.ts`. Previewer packaging: `examples/previewers/lib/bundle.mjs`. Compare: units. SVG labels: one pure module in the web client (`lib/svgLabels.ts`). |
| Defaults and fallbacks | Without the previewer installed (or off for a member) the file opens as text, as today. An unknown shape is a box. A region on a bitmap stays a region. |
| External contracts | None new: two package manifests and the existing frame protocol (`selection`, `units`, `toolbar`). Older hosts ignore capabilities they predate. |
| Load on users and agents | None: the member sees the diagram and clicks a box; the agent gets "Box “X” (id b)" instead of pixels. |
| Release and compatibility | Components, rolled out `optin` first, then `auto`; the SVG label change is web-only (a deploy). Nothing reads either file differently until a previewer is installed. |
| Observability | `preview.error` shows in the frame; the host logs nothing new. |

**Pre-mortem.**

1. **"It renders, but not like diagrams.net."** A shape or style the table
   lacks. It still draws a labelled box; the table grows from real files.
   The sample files in the repo are drawn from what agents and members
   produced.
2. **"The agent edits the wrong box."** The detail names the id and the
   text; Mermaid's detail names the source lines. A click selects the box
   under the pointer, not its group (a swimlane's children are picked
   before the lane).
3. **"A big diagram freezes the panel."** The cell cap and the page cap
   bound it; the rest is named in a notice, not silently dropped.

## 5. Milestones

- **P1:** `mermaid` previewer (render, theme, zoom/fit, errors, selection,
  units), tests, bundle checks.
- **P2:** `drawio` previewer (parse, inflate, render, shape table, pages,
  selection, units, reveal), sample files, tests.
- **P3:** SVG region labels in the built-in image viewer.
- **P4:** CI wiring, `docs/architecture/previewer-components.md` list,
  publish `optin` to QA and verify in the workbench against real files.
