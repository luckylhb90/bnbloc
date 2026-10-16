# Covers: a picture on every file's card (2026-10-16)

Status: levels 1–3 and the Office covers implemented (2026-10-16).
Builds on the stored version thumbnail (`services/room/artifact_files.go`,
`docs/architecture/previewer-components.md` capability `thumbnail`).

## 1. Why

A room's file list showed a picture for images, sprite sheets and apps, and a
type icon for everything else: PDFs, Mermaid and draw.io files, source files,
Markdown, tables, JSON, and Office files. The mechanism for a picture existed
(a version's stored thumbnail: PNG or WebP, at most 1024 px a side and 512 KiB,
the first one kept, a higher-ranked source may replace it), but only the
reference sprite-sheet previewer used it.

## 2. Rules

1. **A cover is a version's stored thumbnail, made by whoever renders the
   file.** The room keeps the first; a previewer's (`source=previewer`) replaces
   one the app drew itself (`source=builtin`). The card shows the stored
   thumbnail, else the image itself, else an app's cover, else a type icon.
2. **Level 1: text files.** A viewer may declare `cover: "text"`. For a file it
   opens as text (source, plain text, Markdown, JSON, CSV) the card draws the
   first lines as a page, on a canvas, from the bytes. The file is read, never
   rendered. It is made for the session and **not stored**: a previewer that
   renders the file better sends its own cover when it is opened, and that is
   the one worth keeping.
3. **Level 2: viewers that render send one when the file is opened.**
   - PDF: the guest sends the top of page 1 (16:10) once; the host stores it
     (`builtin`) when the version has none.
   - Mermaid: the diagram on paper, light theme whatever the member's, from a
     second render whose labels are SVG text (a label in a foreignObject taints
     a canvas in WebKit).
   - draw.io: a schematic drawn from the cells' geometry (boxes, ellipses,
     rhombi, arrows ending at their box's edge, short labels, a lane's title on
     top), not maxGraph's drawing (same taint, and detail a 240 px card cannot
     show).
   - Both previewers declare the `thumbnail` capability (version 1.1.0).
4. **One canvas, one size.** `web/previewer-sdk/src/cover.ts` owns the cover size
   (480×300 at 2×), the paper, and the text cover; a previewer draws on it with
   `renderCover`.
5. **A previewer's cover is stored as a previewer's.** The upload names its
   source (`PUT …/thumbnail?source=previewer|builtin`); until now the app sent
   none, so a previewer's cover ranked as the app's.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | Any member who can read the file stores its cover; a visitor's write is refused and ignored. The cover shows what the version holds, to everyone who can see the card: a text excerpt is the file's own first lines, no more than opening it shows. |
| Time and lifecycle | A cover belongs to a version; a new version has none until rendered. A cover stored before a better renderer exists is replaced by it (higher rank), never by an equal one. |
| Scale and cost | Level 1 reads at most 64 KiB of a file of at most 512 KiB, for a card in view, once per session. Levels 2–3 cost one PNG (~40–100 KB) per version, once; level 3 renders at most 24 files per session, one at a time. |
| Failure and recovery | A diagram that does not draw, a canvas that cannot be read back, a refused write: the card keeps its icon. Nothing blocks opening the file. |
| Boundaries and privacy | Text covers are pixels drawn from text the member can already read. The PDF guest posts PNG bytes only, bounded to the room's limit and read through the validated guest reader. |
| Abuse and untrusted input | The excerpt is drawn as text on a canvas, never as markup. The room validates the PNG by magic bytes and dimensions. |
| One owner | The size and text cover: the SDK's `cover.ts`. Which viewer gets a text cover: the viewer's own `cover` field, not a list in the card. Rank: the room. |
| Defaults and fallbacks | A viewer without `cover` and without a cover sent: the type icon, as before. |
| External contracts | The thumbnail route is unchanged (`source` was already accepted). Previewers 1.1.0 add a capability; older hosts ignore it. |
| Load on users and agents | None. |
| Release and compatibility | Web alone for levels 1 and the PDF; the previewers publish `optin` first. Covers fill in as files are opened. |
| Observability | None new. |

## 4. Level 3: files nobody has opened

A card used to show an icon for a PDF, a diagram or an Office file until someone
opened it once. Now a card in view whose version has no cover, and whose viewer
sends one when it renders it (`cover: "render"`: a PDF, or an installed
previewer that declares `thumbnail`), has the file rendered once, out of sight,
in the sandbox the viewer always runs in; the viewer stores the cover itself.
This reverses the old rule that a card never renders its file, under limits that
keep the cost and the exposure where they were: one job at a time, 25 seconds at
most, a version tried once per session, at most 24 jobs per session, files up to
8 MiB, only for a card in view and only for a viewer that renders in a sandbox
(never an app). A visitor's write is refused and ignored. Other members of the
room do the same until the first cover is stored.

## 5. Office

The Office previewers (1.4.0) send covers drawn from the text their units
already read, on the same canvas: the first slide's title and lines on a slide,
the first blocks on a page (headings bold, a table as its first rows), the first
rows and columns of the first sheet as a grid (a merged range once, text running
on over empty cells). No renderer, so nothing to taint a canvas.
