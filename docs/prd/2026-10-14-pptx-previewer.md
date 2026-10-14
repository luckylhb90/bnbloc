# A PowerPoint previewer (2026-10-14)

Status: implemented (2026-10-14), not yet published. Builds on
`docs/prd/2026-10-09-preview-workbench-and-previewer-components.md`.

## 1. Why

Agents in rooms produce slide decks (`.pptx`) as often as documents, and
members attach them. Today a deck opens as a download card: to see what the
agent made, a member downloads it and opens PowerPoint or Keynote, then
comes back to the room to ask for a change. Every other common deliverable
(images, PDF, CSV, Markdown, sites) already previews in place.

## 2. Rules

- **A previewer component, not platform code.** The deck viewer is an
  installable previewer package (`bnbloc.previewer.package/v1`, runtime
  `bundle`) built from `examples/previewers/pptx`, published with
  `bnbloc previewers publish`. The gateway, web host and SDK do not change:
  the host already hands a matching file's bytes to the sandboxed frame.
- **What it claims.** `.pptx` (and the macro-enabled `.pptm`, slideshow
  `.ppsx` and template `.potx`, the same OOXML package). Not the legacy
  binary `.ppt` (an OLE compound file no browser renderer reads reliably):
  it stays a download card.
- **How it renders.** `@aiden0z/pptx-renderer` (Apache-2.0, pinned exact)
  (its ESM build, bundled with our pinned ECharts/JSZip by esbuild) parses
  the package in the frame and renders every slide as HTML/SVG in a
  scrolling list, mounted lazily (only slides near the viewport are in the
  DOM; media and slide shapes decode on demand) so a 200-slide deck opens
  as fast as a 5-slide one. Charts render through the library's ECharts
  build; SmartArt and EMF art render from their stored previews where the
  deck has them.
- **Toolbar.** Zoom (host-rendered zoom control; 100% = slide fits the
  frame width) and Fit. The frame title reads
  "<file> — slide N of M" and follows scrolling.
- **Failure is a sentence.** An encrypted deck (a password-protected
  `.pptx` is an OLE container, not a zip), a legacy `.ppt` renamed to
  `.pptx`, a corrupt package or one over the parse limits reports one plain
  error through the SDK; the host shows it beside the download action. A
  slide that fails alone shows an in-place "This slide could not be
  rendered" panel; the others still render.
- **No new load on agents.** Agents keep writing `.pptx` files exactly as
  today; nothing in prompts, tools or guidance changes.

## 3. Design review (docs/design-review.md)

1. **Actors.** Every viewer of an artifact: room members, the workbench,
   the works gallery, signed-in share pages. Installing is a super-admin
   action; a tenant enables it like any component. Viewers who lack the
   component see the download card, as today.
2. **Time and lifecycle.** A new version of the file arrives as a new
   `preview.file`: the previous viewer is destroyed (its blob URLs, chart
   instances and observers released) before the new one opens. A file
   that arrives while the previous one is still parsing cancels it
   (AbortSignal) so a slow old version can never overwrite a newer one.
3. **Scale and cost.** Bundle ≈1.3 MB (≈0.4 MB gzip), under the 16 MB
   previewer cap, fetched only when a deck opens and cached by sha256.
   Large decks: lazy slides + lazy media + windowed list mounting. The host
   already caps the bytes it transfers.
4. **Failure and recovery.** Parse failure → `reportError`; per-slide
   failure → in-place panel; a failed render never blanks the frame
   silently.
5. **Boundaries and privacy.** The frame's CSP forbids network
   (`connect-src 'none'`, images only from blob:/data:): linked (external)
   pictures and fonts in a deck are not fetched; the deck's own bytes never
   leave the frame. Embedded fonts load from bytes (`FontFace` with an
   ArrayBuffer), no URL fetch.
6. **Abuse and untrusted input.** A deck is untrusted. Zip-bomb and
   resource limits: the library's `RECOMMENDED_ZIP_LIMITS` and its
   embedded-font limits. XML is parsed with `DOMParser` (no entity
   expansion to external resources). A slide's hyperlink to another slide
   scrolls there; an external one is an `<a target="_blank" rel="noopener">`
   (http/https/mailto only, the library's check), which the frame's sandbox
   (no `allow-popups`) refuses to open, so the frame never navigates away. The bundle has no `eval`; the library's two
   `new Function`/`new Worker` sites are a setImmediate polyfill branch and
   the optional PDF.js worker, which is disabled.
7. **One rule, one owner.** The bundle build (esbuild + deterministic
   ustar + package placeholder) moves from the sprite-sheet build into
   `examples/previewers/lib/bundle.mjs`, used by both previewers and
   tested once.
8. **Defaults and unknowns.** Fonts not embedded and not installed fall
   back to the system's fonts (layout may shift slightly; the renderer
   keeps text boxes). PDF.js fallback for EMF previews is off (`pdfjs:
   false`): such art shows the library's placeholder.
9. **External contracts.** The library is pinned exactly with a lockfile;
   its license and the bundled dependencies' notices (Apache-2.0 ECharts,
   MIT/GPLv3-dual JSZip used under MIT, MPL-2.0 mtx-decompressor
   unmodified) ship in the bundle as `NOTICES.txt`.
10. **Load on users and agents.** Members click a deck and see it; no
    setting. Agents: none (rule above).
11. **Release.** A new component; no platform release. Publish with
    `--rollout optin` first, check a real agent-made deck on QA, then auto.
12. **Observability.** Open failures reach the host through `preview.error`
    (shown to the member); the console logs per-slide errors.

## 4. Verification

- `node --test examples/previewers/pptx/build.test.mjs` (in CI beside the
  sprite-sheet test): deterministic bundle, package shape, claimed
  extensions, no inline script, no network calls, notices present.
- Before publishing, a headless-browser check frames the built bundle like
  the host does (sandbox `allow-scripts`, the gateway's previewer CSP),
  sends `preview.init` + `preview.file` and looks at the slides: done on
  2026-10-14 for `samples/deck.pptx` (title, bullets, table, chart, shape),
  an agent-made 6-slide deck with charts and an 85-slide 8 MB template with
  embedded fonts (opened in 0.7 s, 6 slides mounted), dark theme, zoom, and
  the three error sentences (encrypted/legacy, not a deck, corrupt).
- On QA: publish optin, open an agent-made deck in a room, the workbench
  and a share page.

## 5. Follow-ups

- Non-image chat attachments open in the preview workbench (today only
  images get a lightbox; a member-attached deck or PDF is a download).
- A first-slide thumbnail for file cards (needs DOM-to-PNG in the frame).
