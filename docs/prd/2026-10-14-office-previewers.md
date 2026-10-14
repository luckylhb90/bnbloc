# Office previewers: PowerPoint, Word, Excel (2026-10-14)

Status: implemented and published to QA (2026-10-14; optin, checked in a
room's workbench, then auto). Builds on
`docs/prd/2026-10-09-preview-workbench-and-previewer-components.md`.

## 1. Why

Agents in rooms produce slide decks, reports and spreadsheets as often as
anything else, and members attach them. Today an Office file opens as a
download card: to see what the agent made, a member downloads it, opens
PowerPoint/Word/Excel, then comes back to the room to ask for a change.
Every other common deliverable (images, PDF, CSV, Markdown, sites) already
previews in place.

## 2. Rules

- **Previewer components, not platform code.** Three installable previewer
  packages (`bnbloc.previewer.package/v1`, runtime `bundle`), built from
  `examples/previewers/{pptx,docx,xlsx}` and published with
  `bnbloc previewers publish`. The gateway, web host and SDK do not change:
  the host already hands a matching file's bytes to the sandboxed frame.
  One package per family keeps each bundle small (a member opening a
  spreadsheet never loads the slide renderer) and each installable alone.
- **What they claim.** The OOXML formats only:
  - PowerPoint: `.pptx`, `.pptm`, `.ppsx`, `.potx`;
  - Word: `.docx`, `.docm`, `.dotx`;
  - Excel: `.xlsx`, `.xlsm`, `.xltx`.

  The legacy binary `.ppt`/`.doc`/`.xls` are OLE compound files that no
  browser renderer reads reliably: they stay download cards. CSV and TSV
  already preview as tables.
- **How they render.** Each is a pinned third-party renderer, bundled by
  esbuild:
  - **PowerPoint:** `@aiden0z/pptx-renderer` (Apache-2.0) renders every
    slide as HTML/SVG in a scrolling list. Slides mount lazily: only those
    near the viewport are in the DOM, and shapes and media decode on
    demand, so an 85-slide 8 MB deck opens in 0.7 s. Charts render through
    ECharts.
  - **Word:** `docx-preview` (Apache-2.0) renders pages: styles, tables,
    images, lists, headers and footers, foot- and endnotes, page breaks.
  - **Excel:** ExcelJS (MIT) reads the workbook; our own code draws the
    active sheet as a grid:
    - values through their number formats (`numfmt`, MIT);
    - fonts, fills, borders, alignment, merged cells;
    - column widths and row heights;
    - hidden rows, columns and sheets left out;
    - sheet tabs.
- **Toolbar.** Zoom on all three (host-rendered), plus Fit to width for
  slides and pages. The frame title follows the view: "<file> — slide N of
  M", "<file> — page N of M", "<file> — <sheet>".
- **Failure is a sentence.** Each of these reports one plain sentence through
  the SDK, and the host shows it beside the download action:
  - an encrypted file (a password-protected OOXML file is an OLE container,
    not a zip);
  - a legacy file renamed to the new extension;
  - something that is not an Office file at all;
  - a corrupt package.

  The sentence names the file and the apps that open it. A slide that fails
  alone shows "Slide N could not be rendered" in place, and the other
  slides still render.
- **Huge sheets say so.** A sheet draws at most 150,000 cells (and 200
  columns). Rows are cut first, and a note names what is shown ("Showing
  rows 1–750 and columns A–T of …; download the file for the rest"). A
  sheet with pictures or charts says they are not shown.
- **No new load on agents.** Agents keep writing Office files exactly as
  today; nothing in prompts, tools or guidance changes.

## 3. Design review (docs/design-review.md)

1. **Actors.** Every viewer of an artifact: room members, the workbench,
   the works gallery, signed-in share pages. Installing is a super-admin
   action, and a tenant enables it like any component. Viewers who lack the
   component see the download card, as today.
2. **Time and lifecycle.** A new version of the file arrives as a new
   `preview.file`. In the PowerPoint previewer the previous viewer is
   destroyed first (blob URLs, chart instances, observers), and an open
   still parsing is aborted. In Word and Excel a generation counter drops a
   superseded render. A slow old version can never replace a newer one.
3. **Scale and cost.** Bundles, under the 16 MB previewer cap, fetched
   only when such a file opens and cached by sha256:
   - pptx 1.3 MB (0.4 MB gzip);
   - docx 0.2 MB;
   - xlsx 1.0 MB.

   Big decks mount lazily, and big sheets are capped (above).
4. **Failure and recovery.** Parse failure → `reportError` with a
   sentence. A per-slide failure gets an in-place panel. Nothing blanks the
   frame silently.
5. **Boundaries and privacy.** The frame's CSP forbids network
   (`connect-src 'none'`; images only from blob:/data:). Linked (external)
   pictures and fonts are not fetched; the file's bytes never leave the
   frame. Embedded slide fonts load from bytes (`FontFace` with an
   ArrayBuffer). docx-preview's embedded fonts would load from blob: URLs,
   which `font-src` refuses, so Word text falls back to system fonts.
6. **Abuse and untrusted input.** Every file is untrusted:
   - **Zip bombs and resource limits:** the pptx renderer's
     `RECOMMENDED_ZIP_LIMITS` and embedded-font limits; the sheet cell cap.
   - **XML:** parsed with `DOMParser` / ExcelJS's SAX parser, with no
     external entity fetches (no network anyway).
   - **Links:** a document's hyperlinks are drawn as real links, and a
     sandboxed frame that followed one would show an outside page in place
     of the file. A shared click guard (`keepLinksInside`) swallows any link
     but an in-document `#anchor`. Slide-to-slide links still navigate,
     because they are not `<a>` elements.
   - **Disabled content:** Word alternate chunks (embedded foreign HTML),
     comments and tracked changes are off.
   - **No eval:** the bundles have no `eval`. The `new Function` sites are
     setImmediate polyfill branches taken only for string callbacks. The
     pptx `new Worker` is the optional PDF.js worker, which is disabled.
7. **One rule, one owner.**
   - `examples/previewers/lib/bundle.mjs` owns the bundle build: esbuild,
     the deterministic ustar archive, the package placeholder, and
     NOTICES.txt generated from what esbuild actually inlined.
   - `lib/office.ts` owns format sniffing, the failure sentences, titles and
     the link guard.
   - `lib/bundle-check.mjs` checks every bundle the same way.
   - One tsconfig type-checks all previewers.
8. **Defaults and unknowns.**
   - Fonts that are neither embedded nor installed fall back to system
     fonts.
   - Excel theme colours use the default Office theme. A workbook with a
     custom theme shows the default palette's colours for theme-coloured
     cells.
   - Formulas show their cached results. A formula never calculated by
     Excel shows nothing, never a guessed value.
   - Bullets Word stores as Symbol/Wingdings private-use code points are
     drawn as the Unicode characters those fonts show (browsers have no
     such glyphs).
9. **External contracts.** Every library is pinned exactly with a
   lockfile per previewer. NOTICES.txt holds the license files of every
   package esbuild inlined, read from the installed copies, so the text
   always matches the bundle. A bundled package without a license file
   fails the build.

   ExcelJS ships a prebuilt browser bundle that already inlines its
   dependencies. Those dependencies are attributed from its dependency tree
   (by license field when a package ships no file). Inline license headers
   are kept at the end of `main.js`.
10. **Load on users and agents.** Members click a file and see it; no
    setting. Agents: none (rule above).
11. **Release.** New components; no platform release. Publish with
    `--rollout optin` first, check real agent-made files on QA, then auto.
12. **Observability.** Open failures reach the host through `preview.error`
    (shown to the member); the console logs per-slide errors.

## 4. Verification

- CI (`scripts/ci.sh web`) installs each previewer with its frozen
  lockfile, type-checks them all and runs
  `examples/previewers/{lib,*/build}.test.mjs`. These check:
  - a deterministic bundle and the package shape;
  - the claimed extensions;
  - no inline script, no network calls;
  - every bundled package attributed in NOTICES.txt;
  - the sniffing and failure sentences;
  - the bullet mapping;
  - the sheet model against the sample workbook read by ExcelJS (formats,
    merges, hidden parts, colours, sizes).
- Before publishing, a headless-browser check frames each built bundle the
  way the host does (sandbox `allow-scripts`, the gateway's previewer CSP),
  sends `preview.init` + `preview.file`, and looks at the result. Done on
  2026-10-14:
  - **PowerPoint:** the sample deck, an agent-made 6-slide deck with charts
    and an 85-slide 8 MB template with embedded fonts; dark theme and zoom.
  - **Word:** the sample document (bullets, table, page break, footer).
  - **Excel:** the sample workbook and two real exports (4,472 and 2,010
    cells).
  - **Error sentences:** encrypted/legacy, not an Office file, corrupt.
  - **Link guard:** clicking an external link leaves the frame on the
    preview.
- On QA: publish optin, open agent-made files in a room, the workbench and
  a share page.

## 5. Chat attachments open in the workbench

A member's attached deck, document, sheet or PDF was a download link (only
raster images had a lightbox). Now any attachment a viewer can show opens
as a workbench tab beside the conversation, with the same viewers as a
deliverable and a download button beside it; files no viewer claims stay
download links, and images keep the lightbox.

- An attachment tab (`f:<attachmentId>`, `lib/workbenchTabs.ts`) is one
  immutable file, not an artifact: no versions, no share link, no live
  state, no stored thumbnail. Viewers get `artifactId: ""`, which the
  previewer host and the app runner read as "no artifact routes".
- Its bytes come from the member-authenticated attachment route through
  `api.attachmentBlob`; the last few are cached (they never change).
- Tabs persist per room like deliverable tabs; a tab whose attachment was
  deleted shows the load error.
- Checked on 2026-10-14 against QA with a local web build: the three
  sample files attached to a message, each opened in its tab and rendered.

## 6. Follow-ups
- A first-page thumbnail for file cards (needs DOM-to-PNG in the frame).
- Excel: charts and pictures; the workbook's own theme colours; virtualized
  drawing instead of the cell cap.
