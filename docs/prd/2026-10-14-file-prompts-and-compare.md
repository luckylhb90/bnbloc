# File prompts and comparing any file (2026-10-14)

Status: F1–F3 implemented (2026-10-14), not yet released; F4 after the
1.2.0 previewers replace the installed 1.1.0 ones. Follows
`docs/prd/2026-10-14-ai-document-editing.md` (M2 shipped `previewer.suggestions`
and the Ask AI panel; M3 local documents) and
`docs/prd/2026-10-14-office-previewers.md`.

## 1. Why

Two gaps were seen on QA:

- **A room's empty state does not know the file in front of it.** Opening a
  deck from the computer lands in "My files" with the workbench showing the
  deck, but the composer offers "Build a landing page". The only file-aware
  prompts are a previewer's `suggestions` (M2), which:
  - are a second, weaker prompt system beside prompt packs: no variables,
    no `/slash`, no usage counts, no organization curation;
  - exist only for formats with an installed previewer (none for Markdown,
    PDF, CSV or images).
- **Compare works for text only.** The files agents edit most (decks,
  documents, sheets) show "Comparing versions is available for text files".
  Members cannot see what an edit changed without opening both versions.

## 2. What exists (checked against the code)

- Prompt packs are content Components (`internal/contentpkg/prompts.go`,
  kind `prompt`): prompts with variables, `slash`, `starter`, tags.
  - Built-in packs are data directories under `internal/contentbuiltin/packages/`.
  - The composer lists choices from the member's, the room's and the Library's
    packs (`web/src/lib/promptPacks.ts`).
  - An empty room shows `starterChoices` (room packs, then built-in).
- The viewer registry (`web/src/views/artifact/registry.ts`) owns:
  - file matching: `matchSpecificity` over MIME, multi-part extension,
    extension, kind and MIME wildcard;
  - ranked selection, and a runtime register/subscribe store.

  Installed previewers register into it from the member catalog.
- Previewer frames speak `web/previewer-sdk/src/protocol.ts`; capabilities are
  a code-owned vocabulary mirrored in Go (`core/pkgmeta`), and the host ignores
  a capability it predates.
- Compare: `VersionDiff` over `diffLines` (LCS of lines), shown only when
  `isTextual(facts)`.
- Package parsing is strict (`pkgmeta.ParsePackage`, unknown fields refused),
  and installed records re-parse at every gateway boot (`extpipe/store.go`).

## 3. Rules

1. **One owner for "does this file match".** A file match spec (`FileMatch`:
   MIME types, extensions) is one wire shape and one Go validator
   (`internal/filematch`), used by previewer packages and prompt packs. On the
   web, one pure module (`lib/fileMatch.ts`) scores a match against a file.
   Viewers, comparers and file prompts all rank through it.
2. **File prompts are prompt-pack prompts with a `files` scope.**
   - A prompt may declare `files` (a `FileMatch`).
   - A prompt with `starter: true` and a `files` match is a **file
     suggestion** for matching files.
   - A starter without `files` stays a room starter, as today.
   - Sources are the packs in the member's reach (theirs, the room's, the
     Library's, built-in), ranked by match specificity, then source, then
     uses; at most six.
   - A built-in pack `file-prompts` ships the defaults for Office, PDF,
     Markdown, CSV and images as data. An organization publishes its own the
     same way it publishes any pack.
3. **Previewers render; they do not carry prompts.** `previewer.suggestions`
   is removed:
   - from the web at once;
   - from the Office packages in their next version;
   - from the Go schema one deploy later, once no installed record carries
     it (strict re-parse at boot).
4. **The file in focus drives the room's suggestions.**
   - The workbench's active file tab publishes the focused file; the room
     composer subscribes.
   - With a focused file and an empty room, the starters are that file's
     suggestions ("For deck.pptx"). Picking one hands it to the file's Ask AI
     panel, which inserts it there (variables included), so the request
     names the file and its edit lands as a version.
   - Without one, the starters are as today.
   - The Ask AI panel shows the same suggestions on an empty thread, through
     the composer's own starter row (one component), not a second chip list.
5. **Opening a local document opens its Ask AI panel.** Same hand-off as
   rule 4, without a prompt.
6. **Compare is a registry of strategies.** A comparer claims files like a
   viewer (a `FileMatch`, a rank, a source) and is one of:
   - `units`: it splits a version into ordered units `{key?, label, text}`
     (a slide, a paragraph, a cell, a page, or the whole text). One diff
     engine compares the two unit lists and one view shows them;
   - `view`: it renders its own comparison (images: side by side and a
     slider).

   Built-in comparers: text, CSV/TSV (cells), images. PDF pages come with
   F3: a PDF is parsed only inside the sandboxed PDF guest frame
   (`views/artifact/pdfFrame.ts`), never in the app's origin, so its text
   is extracted there, through the same hidden-frame channel as a
   previewer's units. The Compare button shows whenever a comparer claims
   the file.
7. **Formats come from Components: the `units` capability.** A previewer
   that declares `units` answers `preview.units.request {id, file, bytes}`
   with `preview.units {id, units}`, in a hidden sandboxed frame (same
   origin, CSP and ticket as a visible one). The host registers it as a
   `units` comparer. "How to read a pptx" lives once, in the pptx previewer.
   The Office previewers (1.2.0) implement it:
   - pptx: one unit per slide, keyed by the slide's id;
   - docx: one unit per paragraph or table, unkeyed;
   - xlsx: one unit per non-empty cell, keyed `Sheet!A1`, the shown value
     plus its formula.
8. **One diff engine.** `diffSequence(a, b, eq)` (generic LCS) replaces the
   body of `diffLines`.
   - Units with keys align by key.
   - Units without keys align by text; a removed run next to an added run
     pairs into changes.
   - A changed unit shows a line diff of its text.
   - The view lists changed units, with unchanged ones collapsed ("12
     unchanged slides").

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | Every room member sees the same file suggestions for a file (room and built-in packs); a member's own packs add theirs. Compare is read-only for anyone who can see both versions. |
| Time and lifecycle | Focus follows the active tab and clears when the tab or workbench closes. A previewer uninstalled mid-session unregisters its comparer with its viewer. A hidden extraction frame lives for one request and is removed after it. |
| Scale and cost | Units are bounded by the protocol: 5,000 units, 200-character keys, 120-character labels, 4,000-character texts, 2 MiB in all; a larger file is cut and says so. The diff guards n×m as today. A sheet compares only non-empty cells. |
| Failure and recovery | No comparer: no Compare button (as today for binaries). An extraction fails or stalls: the view says so and offers the download, nothing else breaks. A bad unit message is dropped by the host validator like any frame message. |
| Boundaries and privacy | Extraction runs in the same sandbox as the preview (opaque origin, no network). Prompt bodies are member-visible content as today. |
| Abuse and untrusted input | Unit text is untrusted: rendered as text (React-escaped), never HTML. A pack's `files` is validated like a previewer's match (lower-case MIME, bounded extension list). |
| One owner | File match: `internal/filematch` + `lib/fileMatch.ts`. Prompts: prompt packs. Format reading: the previewer package. Diff: `diffSequence`. Registry mechanics: one generic registry used by viewers and comparers. |
| Defaults and fallbacks | No file prompts match: the panel shows its hint and no row. Units without keys fall back to text alignment. A comparer that cannot read one of the versions (a renamed type) falls back to "not comparable" for that pair. |
| External contracts | Prompt packs gain optional `files` (old gateways refuse packs that use it, so the built-in pack ships with the server). Previewer protocol gains the `units` capability and two messages. `previewer.suggestions` leaves in two steps (rule 3). Wire `ComponentPreviewerMatch` becomes `FileMatch` (same JSON). |
| Load on users and agents | Members click a suggestion instead of typing; nothing new to learn. Agents see nothing new. |
| Release and compatibility | Order: server (pack `files`, built-in pack, web), then previewers 1.2.0 (`units`, no `suggestions`) as optin, then auto, then the Go removal of `suggestions`. An older host ignores `units` and keeps rendering. |
| Observability | Compare logs which comparer ran and its unit counts on failure (console); the existing component metrics cover installs. |

**Pre-mortem.** A month later it failed. The three most likely reasons:

1. **"Compare shows everything changed."** Paragraph lists shift on one
   insertion, or an agent rewrote the zip so ids changed. Unkeyed units align
   by text, not position, and pptx keys are slide ids, not file order. The
   engine is tested on an insertion in the middle.
2. **"Suggestions are generic again."** A pack with broad matches (`*/*`
   is refused; `text/*` is not) floods every file. Ranking puts exact MIME and
   extension above wildcards, and the list is capped at six.
3. **"Compare hangs on a big sheet."** Unit and byte caps hold in the frame
   and again in the host validator; a cut list says "showing the first
   5,000 cells".

## 5. Milestones

- **F1:** shared file match (Go + web), generic registry, prompt `files`,
  built-in `file-prompts` pack, file suggestions in the panel and the room
  starters, local open opens the panel, web stops reading
  `previewer.suggestions`.
- **F2:** compare registry, `diffSequence`, unit view, built-in comparers
  (text, CSV, image).
- **F3:** `units` capability (SDK, Go vocabulary, hidden frame runner), Office
  previewers 1.2.0 (units, no suggestions), PDF pages through the PDF guest.
- **F4 (after F3 is installed everywhere):** remove `previewer.suggestions`
  from the Go schema.
