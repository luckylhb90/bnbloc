# Preview workbench and previewer components (2026-10-09)

Status: phases 1–3 implemented (2026-10-09); phase 4 (live app preview) and external share links pending a product decision.

Reference: WorkBuddy's right-hand preview (tabs of open files, file cards with
thumbnails in the conversation, "view all deliverables (N)", type-specific
viewers such as a sprite canvas with zoom / grid / layers / fit controls,
expand / pop-out). Requirement from the product owner: preview types are
open-ended, so viewers must be installable as Components.

## Today (2026-10-09 survey)

- Artifacts are versioned, room-scoped **text** (MEDIUMTEXT, 2 MiB). The
  bridge publishes only known text extensions after/during turns (≤ 24 files);
  binaries are dropped. Kinds: markdown | code | image(url) | html | app.
- One artifact at a time in the dock (inline / overlay / full, desktop
  pop-out). Docs panel = list ↔ detail. ArtifactCard in chat is icon-only,
  attached to a message by a time heuristic. No thumbnails, grid, zoom,
  share, syntax highlighting, PDF, live dev-server preview.
- `app` artifacts run in a sandboxed opaque-origin iframe (no network) with a
  validated postMessage protocol and a room-relayed live-state channel.
- A renderer registry exists (`web/src/views/artifact/registry.ts`) but is
  compile-time only; artifacts carry no MIME type.

## Design

### 1. Deliverables become files (binary-capable artifacts)
- An artifact version is either inline text (as today) or **blob-backed**
  (content in the existing blob store, sha256-addressed): `mimeType`,
  `sizeBytes`, `fileName`, optional `thumbnailBlobKey` per version.
- Size cap per version: deployment config with a documented default; same
  blob path/quotas as attachments.
- The bridge publishes any workspace file the session changed (text or
  binary), with MIME detection (sniff + extension), honours ignore rules
  (`.git`, `node_modules`, build outputs, files over the cap), and links each
  publish to the job/message that produced it (explicit `sourceSeq` /
  `jobId`), replacing the time heuristic.
- `artifact_updated` keeps metadata only.

### 2. Preview workbench (web, desktop and browser)
- Right-side **workbench with tabs**: open several files; tabs persist per
  room; drag to reorder; pin; close others. Modes keep today's inline /
  overlay / full and the desktop pop-out; add an in-app fullscreen toggle.
- Toolbar per tab: version picker (any-to-any compare, restore as a new
  version), download, copy link, open in new window, viewer-contributed
  controls (zoom, fit, grid, layers…).
- **File cards** in the conversation: thumbnail (image itself, or a
  viewer-generated thumbnail), name, size, kind; grid of up to 6 with
  "View all deliverables (N)" → a filterable grid/list of every artifact in
  the room (kind / author / date / search).
- **Links**: room-member deep links to an artifact (+ version), usable in
  chat and in notifications. (Public/expiring external share links are a
  separate decision — not in this scope.)

### 3. Built-in viewers
Image (zoom / pan / fit / pixel grid / checkerboard background, animated
GIF), SVG (sanitized, rendered as image), Markdown (existing), code with
syntax highlighting + line numbers + wrap, HTML/app (sandboxed runner with
reload, fullscreen, device widths), PDF (pdf.js in the sandboxed runner),
CSV/TSV table, JSON tree, audio/video, plain text; anything else → file card
with download.

### 4. Previewer components (`bnbloc.previewer.package/v1`)
- A package declares `previewer: {match: {mimeTypes, extensions}, rank,
  entry (HTML/JS bundle artifact), capabilities: [thumbnail, liveState,
  toolbar], toolbar: [{id, icon, label, kind: button|toggle|zoom}]}` and is
  installed / rolled out / tenant-scoped like every component (signed,
  sha256-pinned artifacts, `?kind=previewer`).
- Runtime: the bundle runs in the same kind of opaque-origin sandboxed
  iframe as the artifact runner (no network: CSP `default-src 'none'`;
  scripts/styles only from the bundle; images/media from blob:/data:). The
  host sends the file bytes (`ArrayBuffer`), name, MIME, version, theme and
  viewport; the previewer answers with rendered UI, toolbar state,
  optional live-state (reusing `artifact_state`) and, if declared, a PNG
  thumbnail the host stores for file cards.
- A small typed SDK (`@bnbloc/previewer-sdk`: `onFile`, `setToolbar`,
  `onToolbar`, `sendState`/`onState`, `provideThumbnail`) and a reference
  previewer (sprite sheet / image atlas viewer with zoom, fit, grid and layer
  controls) prove the contract.
- Selection: most specific match (MIME exact > extension > wildcard), then
  rank; built-ins and installed previewers share one registry; the member
  can switch viewer from the tab menu ("Open with…").
- Desktop: previewer bundles are served from the app's own scheme (like the
  artifact runner) after sha256 verification; browser: from the gateway
  artifact route into the sandboxed frame.

### 5. Live preview of running apps (later phase — decision pending)
Agents' dev servers (on the owner's machine or inside the agent sandbox VM)
exposed to room members through an authenticated relay tunnel, opened in a
workbench tab with reload / open-in-browser. Needs its own security design
(owner consent per port, member auth, no internet exposure).

## Phases
1. Binary-capable artifacts + explicit message links + bridge publishing of
   any changed file.
2. Workbench (tabs, toolbar, versions, links) + file cards with thumbnails +
   "all deliverables" grid + built-in viewers.
3. Previewer components + SDK + reference previewer + thumbnails from
   previewers.
4. (Pending decision) live app preview over the relay.

## Constraints
Core chat/agent flows unchanged; existing text artifacts keep working (new
fields optional); new migration ids only (migrations.lock); every viewer runs
sandboxed; public repo keeps no vendor names.

## Contracts (fixed before implementation)

**Artifact model (additive).** `Artifact` gains `mimeType`, `fileName`
(display + extension source). `ArtifactVersion` gains `storage: "inline" |
"blob"`, `blobKey` (sha256 key in the blob store; never exposed to
non-members), `sizeBytes`, `mimeType`, `sourceJobId`, `sourceSeq` (message
that produced it), `thumbnail: {mimeType, width, height}` (present when one
was stored). Inline text versions behave exactly as today (`content`).

**Routes (room service unless noted; membership-checked, tenant-scoped).**
- `GET /v1/rooms/{room}/artifacts/{id}/versions/{n}/content` → raw bytes with
  the version's MIME type, `Content-Length`, `nosniff`, CSP `default-src
  'none'; sandbox`, `Content-Disposition` inline for safe raster images and
  `attachment` otherwise (viewers fetch it, so disposition does not matter
  to them); works for inline versions too (text bytes).
- `PUT /v1/rooms/{room}/artifacts/{id}/versions/{n}/thumbnail` (PNG/WebP,
  ≤ 512 KiB, ≤ 1024 px, any member; first write wins per version, later
  writes ignored unless the stored one came from a lower-priority source) and
  `GET …/thumbnail`.
- `POST /v1/rooms/{room}/artifacts/{id}/versions` accepts either the current
  JSON `{content}` or `multipart/form-data` (`file` + `baseVersion`,
  `summary`, `sourceJobId`, `sourceSeq`) for blob-backed versions; size cap
  `BNBLOC_ARTIFACT_MAX_BYTES` (documented default 100 MiB).
- `GET /v1/rooms/{room}/artifacts?…` gains filters `kind`, `mime`, `author`,
  `sourceSeq`, `q`, and paging; responses include the new fields.

**Artifact contract details (Phase 1, as implemented).**
- *Kinds.* `markdown | code | image | html | app | file` — `file` is new:
  any other deliverable (PDF, archive, audio/video, office, unknown binary),
  always blob-backed. Kind is the coarse group; pick the viewer by the
  version's `mimeType` (then the extension of `fileName`). `image` versions
  are now blob bytes (older inline `image` versions carry a URL as text).
- *Create.* `POST …/artifacts` accepts `fileName` (last path element kept).
  JSON version publish also accepts `sourceJobId` (≤ 128) and `sourceSeq`
  (≥ 0). Inline versions get `mimeType` from `fileName`/`title`, else kind.
- *Fields.* `sizeBytes` is always set on new versions (inline: UTF-8 byte
  length). `contentHash` is the sha256 of the version's bytes for both
  storages. `sourceSeq` = the room message (chat seq) that **prompted** the
  agent job; `sourceJobId` = that job. Both empty for manual publishes.
- *Multipart publish.* Fields `baseVersion` (required), `summary`,
  `sourceJobId`, `sourceSeq` in any order relative to `file`. MIME = the
  part's `Content-Type` unless empty/`application/octet-stream`, else from
  the file name (part filename → artifact `fileName` → `title`), else
  sniffed. A declared PNG/JPEG/GIF/WebP must match the sniffed bytes (415).
  Over the cap → 413. Same sha256 + MIME as the current version → `200
  {artifact, version: <current>, unchanged: true}` (no new version, no
  event). CAS conflict → 409. Responses: `{artifact, version}`.
- *Content route.* `Content-Type` is the version MIME (`; charset=utf-8` for
  `text/*` and JSON); `Content-Disposition` inline only for sniff-verified
  PNG/JPEG/GIF/WebP, `attachment; filename=…` otherwise; `ETag` =
  `"<contentHash>"`, `Cache-Control: private, max-age=31536000, immutable`
  (versions are immutable); `If-None-Match` → 304.
- *Thumbnail PUT.* Raw body (not multipart), `Content-Type: image/png |
  image/webp` (else 415); magic bytes + header dimensions checked (415 on
  mismatch), each side 1..1024 px (400), ≤ 512 KiB (413). `?source=builtin`
  (default; a built-in viewer's or host-generated thumbnail) or
  `?source=previewer` (an installed previewer's `preview.thumbnail`); a
  write replaces the stored one only from a strictly higher source
  (previewer > builtin), else it is ignored. Response `200 {version,
  stored: bool}`; a stored write publishes `artifact_updated` with
  `reason: "thumbnail"`. `GET …/thumbnail` → bytes (inline, `ETag`,
  `Cache-Control: private, no-cache`), 404 when none. No server-side image
  decoding: raster images are their own thumbnail (the web may use the
  content route for image versions).
- *List.* Filters `channelId`, `kind`, `mime` (exact or `type/*`), `author`
  (artifact creator), `sourceSeq` (artifacts with ANY version from that
  message), `q` (title or fileName substring, case-insensitive); `limit`
  1..200 (default 100); order `updatedAtMs desc, artifactId desc`; response
  `{artifacts, maxBytes, nextCursor?}` — pass `cursor=<nextCursor>` for the
  next page (`nextCursor` present only when the page is full). `maxBytes` is
  the deployment's blob version cap (`BNBLOC_ARTIFACT_MAX_BYTES`).
- *Event.* `artifact_updated` payload adds `mimeType`, `fileName`, and
  optional `reason`.
- *Search.* `GET /v1/search` matches artifact titles **and file names**;
  artifact hits add `artifactKind`, `mimeType`, `fileName`.

**Previewer package** `bnbloc.previewer.package/v1`, contract
`bnbloc.previewer.contract/v1`, runtime `bundle`: artifacts `bundle`
(a .tar/.zip or single .html entry with its assets, sha256-pinned),
`previewer: {entry, match: {mimeTypes[], extensions[]}, rank, capabilities
[thumbnail, liveState, toolbar, edit?], toolbar[...]}`. Member catalog
lists installed previewers (rollout/tenant gated) with their match and the
bundle artifact URL.

**Frame protocol (host ↔ previewer, postMessage, opaque origin).**
host→frame: `preview.init {protocol:1, file:{name, mimeType, sizeBytes,
version}, theme, viewport}`, `preview.file {bytes: ArrayBuffer}`
(transferred), `preview.toolbar {id, value}`, `preview.state {state}`,
`preview.resize {w,h}`. frame→host: `preview.ready`, `preview.toolbar.set
{items}`, `preview.state.send {state}` (relayed as artifact_state, 16 KiB),
`preview.thumbnail {png: ArrayBuffer}`, `preview.error {message}`,
`preview.title {text}`. Every message validated (source frame, opaque
origin, known type, size limits).

**Previewer contract details (Phase 3, fixed before implementation).**
- *Package.* `runtime: "bundle"`, exactly one artifact named `bundle`; no
  endpoint / sidecar / manifest / hooks / protocol / localSettings / guest and
  no `permissions.network` (a bundle never gets network access). `previewer`:
  `entry` — a relative `.html` path inside the bundle (`[A-Za-z0-9._-]`
  segments, no `.`/`..`, ≤ 200 chars); `match.mimeTypes` (`type/subtype` or
  `type/*`, lowercase, never `*/*`) and `match.extensions` (`.ext`, lowercase,
  up to three dot parts such as `.atlas.json`) — at least one entry overall,
  ≤ 32 each, no duplicates; `rank` 1–1000 (ordered like the web registry's
  built-ins, rank 10); `capabilities` ⊆ `thumbnail, liveState, toolbar` (code
  owned; `edit` is reserved, not accepted yet); `toolbar` ≤ 20 items `{id
  ([a-z][a-z0-9-]{0,31}, unique), icon (code-owned name list in
  web/previewer-sdk/src/protocol.ts), label (1–40), kind button|toggle|zoom}`
  and a non-empty toolbar requires the `toolbar` capability.
- *Bundle.* An uncompressed POSIX ustar archive (`.tar`) of regular files
  (directories implied, no links/devices), ≤ 16 MiB, ≤ 512 files, every path
  valid as above, containing `entry`. A single-file previewer is a tar with
  one `.html`. Verified at install (the uploaded artifact's sha256, the
  archive parses, `entry` present).
- *Member catalog.* `GET /v1/components[?kind=previewer]` (rollout + tenant
  gated like every kind) lists an installed previewer with
  `previewer: {entry, match, rank, capabilities, toolbar, bundleUrl,
  bundleSha256, frameUrl, frameExpiresAtMs}`: `bundleUrl` is the gateway
  artifact URL of the archive (the desktop downloads it and checks
  `bundleSha256`); `frameUrl` is the browser frame base
  `/v1/previewers/{name}/{version}/{ticket}/` (gateway-relative; the host
  appends `entry`).
- *Browser serving.* `GET /v1/previewers/{name}/{version}/{ticket}/{path…}`.
  An iframe `src` cannot carry the session bearer and a sandboxed frame's
  subresource loads carry no usable cookie, so the path carries a short-lived
  (1 h) signed frame ticket (session-key HMAC, token kind
  `previewer-frame`, bound to subject + tenant + `name@version`); relative
  asset URLs inherit it. Every request re-checks the rollout/tenant gate and
  that `version` is the installed one, then serves the file from the
  verified archive (sha256 re-checked on load, cached by hash) with
  `Content-Security-Policy: default-src 'none'; script-src 'self' <bundle
  base>; style-src 'self' <bundle base> 'unsafe-inline'; img-src blob: data:;
  media-src blob: data:; font-src 'self' <bundle base> data:; connect-src
  'none'; frame-ancestors 'self'`, `nosniff`, `no-referrer`,
  `Access-Control-Allow-Origin: *` (module scripts and fonts load in CORS mode
  from the opaque origin), `Cache-Control: private, no-cache`. Path
  traversal, unknown files, a bad ticket, or a gated component → 404.
- *Desktop serving.* `bnbloc-previewer://localhost/{name}/{version}/{path…}`
  (Windows: `http://bnbloc-previewer.localhost/…`): the shell resolves the
  previewer from its member catalog, downloads `bundleUrl` with its session,
  refuses a sha256 mismatch, and serves files from the verified archive with
  the same CSP (bundle base = the scheme origin, frame-ancestors = the app
  origin). The app CSP `frame-src` admits this scheme.
- *Frame.* `<iframe sandbox="allow-scripts">` (never allow-same-origin) at
  `<frame base><entry>#host=<host origin>`. The SDK accepts messages only
  from `window.parent` with that origin (it posts with target `"*"`: a
  custom-scheme app origin is not reliably matchable, `frame-ancestors` pins
  the parent, and nothing it sends is secret); the host accepts messages
  only from that frame's window with origin `"null"`. The SDK posts `preview.ready` on
  window load; the host answers `preview.init` then `preview.file`.
  `preview.toolbar.set` items follow the toolbar rules plus state (`active`
  for toggles, `value` zoom factor 0.01–64, `disabled`). Gestures:
  `preview.toolbar {id, value}` with `null` (button), boolean (toggle),
  number (zoom). `preview.state {state, actor}` delivers room live state.
  `preview.thumbnail` must be a PNG ≤ 512 KiB; the host PUTs it to the
  version's thumbnail route only when the previewer declares `thumbnail` and
  the version has none.

**Web viewer registry (Phase 2, as implemented).** One registry
(`web/src/views/artifact/registry.ts`) holds built-in viewers
(`builtin.ts`) and installed previewer components alike.
- *Registration.* `registerArtifactViewer({id, label, match: {mimeTypes?,
  extensions?, kinds?}, rank, input: "text" | "blob", safety: "safe" |
  "sanitized" | "sandboxed", source: "builtin" | "component", maxBytes?,
  load})` returns its unregister; re-registering an id replaces it;
  `unregisterArtifactViewer(id)`; `subscribeArtifactViewers` +
  `registryRevision` (useSyncExternalStore) so open tabs re-select when a
  component is installed or removed at runtime. `load` resolves to a React
  component receiving `ArtifactViewerProps`.
- *Selection.* `selectViewers(file)` lists every candidate, best first:
  specificity exact MIME (4) > extension (3, multi-part `.atlas.json` match
  the name's end) > legacy kind (2) > MIME wildcard `type/*` (1); then
  `rank` **ascending** (lower wins; built-ins use 10, plain text 20); then a
  component before a built-in; then id. A viewer whose `maxBytes` is below
  the version's `sizeBytes` drops out. `selectViewer(file, preferredId)`
  honours the member's "Open with…" choice while it is still a candidate
  (stored per tab as `viewerId`). No candidate → a download card; inline
  text with no candidate → the plain-text viewer.
- *File info.* `{name, mimeType, extension, kind, language?, sizeBytes?,
  version}`: MIME is the version's `mimeType`, else the artifact's, else
  inferred from the extension of `fileName`/`title`/code `language`, else
  from kind (`markdown`, `app` → text/html, `table`). Legacy inline `html`
  (no MIME) is NOT inferred as text/html: it stays source (code viewer);
  html files with a MIME open in the sandboxed runner.
- *Props.* `{roomId, actor?, artifactId, version, file, blob, content,
  toolbar, onLocalState?, remoteState?}` — `blob` is the version's bytes
  (content route for blob versions, the version route's `content` for inline
  ones), typed with the effective MIME; `content` is the decoded text for
  `input: "text"` viewers ("" otherwise).
- *Toolbar.* `toolbar: {set(items), subscribe(handler)}`. Items are the frame
  protocol's `ToolbarItem` shape (`{id, icon, label, kind: button | toggle |
  zoom, active?, value? (zoom factor), disabled?}`) plus a built-in-only
  `select` kind (`options`, `selected`). Gestures arrive as `(id, value)`
  with `null` / boolean / zoom factor / option value. The tab renders items
  generically (icon names → host icons; unknown names render the label), so
  a remote previewer forwards `preview.toolbar.set` to `set()` and relays
  gestures back as `preview.toolbar`.

## Implementation notes

### Phase 1

Backend landed (binary-capable artifacts, source links, bridge publishing of
any changed file). Contract details are in "Artifact contract details" above.

- **Blob owner.** realtime stays the single owner of blob storage
  (`BNBLOC_ATTACHMENTS_*`, disk volume or S3). The room service stores and
  reads blob versions/thumbnails through realtime's existing signed internal
  route (`POST/GET /internal/v1/blobs`) via `internal/realtime.HandoffClient`
  (`PutBlob` / `OpenBlob`, the same client the handoff already uses). No new
  blob env, no shared volume, no S3 credentials on room. Room access control
  is membership on the room routes; the gateway's `/v1/artifacts/{key}`
  entitlement gate does not grant room blob keys.
- **Cap.** `BNBLOC_ARTIFACT_MAX_BYTES` (room; default 100 MiB, must be
  1..256 MiB = realtime's blob put cap, else fatal at startup). Wired in
  compose, prod overlay, Helm (`room.artifactMaxBytes`), the all-in-one
  entrypoint (pass-through), both env docs; the Caddyfile and ingress
  annotations allow 256 MiB on `POST /v1/rooms/*/artifacts/*/versions`.
- **Schema.** Migrations `2026_10_preview_artifact_files` (artifacts
  `mime_type`, `file_name`; versions `storage`, `blob_key`, `size_bytes`,
  `mime_type`, `source_job_id`, `source_seq` + index `(tenant_id,
  source_seq)`) and `2026_10_preview_thumbnails` (`thumb_blob_key`,
  `thumb_mime_type`, `thumb_width`, `thumb_height`, `thumb_rank`). Existing
  rows read as inline with `sizeBytes` 0 (dev data is disposable).
- **Typing.** One table in `internal/store/artifact_files.go`
  (`ArtifactTextMIME`, `ArtifactMIME`, `ArtifactKindForFile`,
  `IsInlineSafeImage`) shared by the bridge and the room service.
  Mapping: `.md/.markdown` → markdown; `.html/.htm` → html (app when the
  document carries `<script`); other known text/code extensions and
  `Makefile`/`Dockerfile`/`LICENSE`/`README` → code; `image/*` (png, jpeg,
  gif, webp, svg, bmp, ico, avif) → image; everything else (pdf, zip, audio,
  video, fonts, office, unknown) → file.
- **Bridge.** Every changed workspace file is a candidate, minus ignore rules
  (any path segment starting with `.` — `.git`, `.venv`, `.room`, caches,
  `.env`; directories `node_modules`, `bower_components`, `vendor`, `venv`,
  `__pycache__`, `target`, `dist`, `build`, `out`, `coverage`) and files over
  the room's `maxBytes` (read from the artifact list before a pass; an
  unreachable gateway defers the pass without losing the changes). Order:
  files written this turn first, then size, then path; ≤ 24 per pass. Known
  text ≤ 2 MiB and valid UTF-8 → inline JSON publish (as before); otherwise
  multipart blob publish with the sniffed MIME. Every version carries
  `sourceJobId` + `sourceSeq` (the job's prompting chat seq, now read from the
  claimed job's `messageSeq`).
- **Binary-safe core reads.** `core.SessionFiles.ReadSessionFile` returns
  bytes; a manifest's `readSessionFile.extract.contentBase64` makes reads
  exact and reports the new capability `sessionFileBytes`. Only such cores
  publish binaries; a text-only core (`extract.content`) still publishes known text files only. Binary
  deliverables from such a core need its daemon to return base64 content and the
  manifest to declare `contentBase64` (follow-up for the core owner).
- **Handoff.** Export writes blob versions as raw files (`v<N>.<ext>`, `.bin`
  fallback) with `storage/mimeType/sizeBytes/sourceJobId/sourceSeq` in
  `artifact.json`; import streams them back into the blob store, re-verifies
  the sha256 (validated before anything is created), and drops source links.
  Thumbnails are not exported. `docs/room-export-format.md` updated.
- **Observability.** `routeLabel` knows `content` and `thumbnail`.
- **Tests.** Store (memory + MySQL): blob versions, MIME tracking, thumbnail
  ranking, filters + keyset paging, file-name search. Room (real room +
  realtime over HTTP with the disk blob store): multipart publish, headers,
  membership, cap (413), raster mismatch (415), CAS (409), dedupe, PDF typing,
  inline content + source links, thumbnails (PNG/WebP validation, ranking,
  bounds), list filters/paging/`maxBytes`, export/import round trip with a
  binary artifact (+ tampered blob refused). Bridge: binary → multipart with
  MIME + source link, ignore rules, cap, text-only cores skip binaries.
  manifestcore: `contentBase64` decoding + capability.

### Phase 2

Web workbench, file cards, deliverables view and built-in viewers.

- **Workbench.** `views/workbench/`: `useWorkbench` (tab model
  `lib/workbenchTabs.ts` — open / activate / close / close others / pin /
  move / per-tab version and viewer, ≤ 12 tabs evicting the oldest unpinned
  inactive; persisted per room in `localStorage["bnbloc.workbench.<room>"]`),
  `PreviewWorkbench` (tab strip: drag to reorder, Alt+Shift+Arrow,
  middle-click / Delete to close, tab menu pin / close / close others, "All
  deliverables" button), `ArtifactTabView` (title + type + size; version
  picker over every version; Compare any two text versions with the line
  diff; "Restore as new version" — JSON publish for inline text, multipart
  re-publish of the bytes for blob versions; Edit for the current inline text
  version; download; copy link; open in new window; fullscreen; viewer
  toolbar). `useDock` no longer knows artifacts — it takes the workbench's
  open flag; inline / overlay / full modes are unchanged. Fullscreen grows the
  same element (no remount, a running preview keeps its state) and Escape
  exits it through the modal layer stack. The desktop pop-out
  (`StandalonePreview`) hosts `ArtifactTabView` for one artifact; popping a
  tab out closes it in the dock. `ArtifactPanel`, `LazyArtifactPanel` and
  `ArtifactCard` are deleted; the Docs rail panel is the deliverables view
  (with "New document / New code file").
- **Links.** `#/room/<id>?artifact=<aid>[&v=n]` (`lib/hashRoute.ts`); App
  hands it to the room through `artifactLinks` (like `messageJumps`) and drops
  it from the URL. Copy link = gateway base (desktop) or the page URL
  (browser) + that hash. Open in new window = desktop pop-out, else a browser
  tab at the link.
- **Shared data.** `views/workbench/artifactData.ts`: artifact detail cached
  per artifact (cards, tabs and the anchoring share one request) and
  invalidated by `artifact_updated` / reconnect; version bytes cached by
  (room, artifact, version) (immutable); card images as object URLs (LRU,
  revoked on eviction). Everything is fetched with the bearer; no credential
  ever goes into a URL.
- **File cards.** `MessageDeliverables` + `FileCard`: up to 6 per message in
  a compact grid, "View all deliverables (N)" opens the deliverables tab
  filtered by `sourceSeq`. Thumbnail = stored thumbnail, else the image's own
  bytes (blob raster/SVG ≤ 8 MiB; tiny images render pixelated), else a type
  icon; detail and images load only when the card scrolls into view.
  Anchoring (`useRoomActivity`): explicit `sourceSeq` of the versions (an
  artifact shows under every message that produced a version); the old time
  heuristic is kept only as a marked fallback for versions without
  `sourceSeq`.
- **Deliverables view.** Grid/list (persisted), search (`q`), author,
  `sourceSeq` and `mime=image/*` sent server-side and re-applied client-side;
  type categories and date ranges client-side; `cursor` paging ("Load
  more").
- **Viewers.** Image (fit / 100% / zoom buttons, wheel, pinch, double-click;
  drag pan; pixel grid from 400% drawn as a viewport-sized overlay;
  checkerboard; GIF/WebP animation kept), SVG as `<img>` from a blob URL,
  Markdown, code (line numbers, wrap, copy; coloring by the app's existing
  dependency-free tokenizer `lib/highlight` — zero added bytes; blocks of 200
  rows with `content-visibility: auto`; coloring skipped above 1 MB), plain
  text, HTML/app (runner fills the tab, reload, device widths 375 / 768 /
  1280 / fill; starts running when the tab opens), PDF, CSV/TSV (RFC 4180
  parser, virtualized rows, header toggle, ≤ 500k rows), JSON tree (lazy
  children, 200 per page, expand-all only under 5000 nodes), audio/video
  (native players from blob URLs); anything else → download card.
- **PDF isolation.** `pdfjs-dist` (legacy build, Apache-2.0) runs inside the
  existing sandboxed runner frame, never in the app origin: the host hands the
  runner a small guest document, then posts the pdf.js library and worker
  *source text* (a lazy `?raw` chunk, ~1.8 MB raw / ~0.55 MB gzip, fetched
  only when a PDF opens) plus the bytes; the guest evaluates them as inline
  module scripts (the runner CSP already allows inline script and nothing
  else), runs pdf.js's worker on the frame's main thread (no Worker from an opaque
  origin), and renders visible pages to canvases. Messages are validated
  (`views/artifact/pdfFrame.ts`). No desktop scheme or gateway change was
  needed. Limits: no text layer/selection; JPX/JBIG2 images and
  non-embedded CJK CMaps need network-loaded assets and do not render.
- **Responsive.** On a compact room the workbench is the existing full-screen
  sheet with the same tab strip (horizontally scrolling tabs, close buttons
  always visible on coarse pointers, wrapping toolbar).
- **Tests.** `lib/workbenchTabs.test.ts`, `lib/artifactFiles.test.ts`,
  `lib/format.test.ts`, `lib/hashRoute.test.ts` (artifact links),
  `views/artifact/{registry,tableData,imageView,pdfFrame}.test.ts`.
  Screenshots at 1280 and 375 against `vite preview` with mocked artifacts
  (PNG, sprite, PDF, TSX v1/v2, CSV, SVG, JSON, Markdown, app).
- **Follow-ups.** Host-generated (`?source=builtin`) thumbnails for PDFs and
  videos; a PDF text layer.

### Phase 3

Status: implemented (installable previewers, SDK, reference previewer).
Standing reference: `docs/architecture/previewer-components.md`.

- **Package kind** (`core/pkgmeta/previewer_validation.go`): `Package.Previewer`,
  `RuntimeBundle`, the code-owned vocabularies mirrored from
  `web/previewer-sdk/src/protocol.ts` (a Go test parses the TS file and fails on
  drift). `ValidateCompleteSidecarArtifacts` became `ValidateCompleteArtifacts`
  (sidecar and bundle runtimes: no upload placeholder at publish).
- **Bundle format** (`internal/tarbundle`): plain ustar only (PAX/GNU
  refused, matching the desktop's hand-written reader); `Pack` is deterministic
  and byte-identical to the reference build script's writer (same sha256).
- **Install** (`internal/previewerruntime`, extreg kind `previewer`, backend
  `bundle`, hosted by the gateway): no activation; the gateway probe reads the
  bundle back from the blob store, checks sha256, parses it, requires `entry`.
- **Gateway** (`services/gateway/previewers.go`): `previewer` block in
  `GET /v1/components` (+ new `?kind=` filter) with a frame ticket (auth token
  kind `previewer-frame`, 1 h, `ID = name@version`); frame route
  `GET /v1/previewers/{name}/{version}/{ticket}/{path...}` re-checks ticket,
  gate and installed version per request, serves from a sha256-keyed in-memory
  cache (64 MiB) with the contract CSP. Artifact download entitlement covers
  previewer bundles (member-catalog kind). Platform list carries `previewer`.
- **Decision — serve by URL, not inline**: verified in Chromium that a
  sandboxed opaque-origin document at the ticketed path loads relative
  scripts/styles under `script-src 'self' <bundle base>`; no asset inlining is
  needed. The explicit bundle base stays in the CSP for engines whose `'self'`
  differs in opaque-origin documents. CSP `frame-ancestors` overrides a
  proxy-wide `X-Frame-Options: DENY` (the sample Caddyfile sets one — note:
  the existing artifact runner page has no `frame-ancestors` and is therefore
  blocked behind that Caddyfile; separate follow-up).
- **SDK posts with target `"*"`** (contract updated above): the desktop app
  origin (`tauri://localhost`) is not reliably matchable as a targetOrigin;
  inbound stays pinned to `#host=`.
- **Desktop**: `ComponentKind::Previewer` (without it the first previewer in
  the catalog would have failed the whole catalog decode and stopped sidecar
  supervision); async `bnbloc-previewer` scheme resolving from the member
  catalog, sha256-verified download, memory + disk cache, purge on reconcile;
  `frame-src` admits the scheme. Unit-tested only — not yet exercised in a
  real WebKit frame.
- **Web**: `lib/previewerFrame.ts` (catalog parsing, frame URLs, inbound
  validation), `views/artifact/{remotePreviewers.ts,RemotePreviewer.tsx}`
  (registry sync at sign-in / tenant switch / 10 min; frame host: init + file
  transfer, toolbar, live state + join snapshot, thumbnail PUT when declared
  and missing, resize, errors), "Open with…" (`OpenWithMenu.tsx`) wired into
  the workbench tab, choice remembered per MIME/extension
  (`lib/viewerPreference.ts`); console shows the kind (filter, facts,
  bundle upload via the artifact step).
- **CLI**: `bnbloc previewers pack <dir> --out <tar>` and
  `bnbloc previewers publish --package <json> (--bundle <tar> | --dir <dir>)`.
- **Reference previewer**: `examples/previewers/sprite-sheet` (images and
  sprite-sheet / atlas JSON; zoom, fit, grid, checkerboard, frame boundaries;
  live layer state; thumbnail). Rank 50: built-ins keep plain images and
  `.json` by default; members switch with "Open with…".
- **E2E** (built web bundle + mocked room APIs, real gateway for catalog and
  frame route): built-in image → Open with → previewer renders; host toolbar
  gestures reach the frame; thumbnail PUT (PNG) and `artifact_state` relay
  observed; atlas frames rendered; the remembered choice reopens the PNG in
  the previewer. Probed in the frame: origin `null`, `fetch` blocked by
  `connect-src 'none'`, storage and `top` access blocked.

### Core session files

Protocol cores (the adapter-daemon form that drives most agents) published no
deliverables: Core Protocol v1 had no session-file endpoints, so
`core.As[core.SessionFiles]` never matched them.

- **Protocol.** Two optional capabilities, advertised together (the wire has
  only the exact-bytes read): `sessionFiles` —
  `GET /core/v1/workspaces/{ws}/sessions/{sid}/files` →
  `ListSessionFilesResponse {files:[{path, sizeBytes, modTimeMs}], truncated}`
  (regular files, workspace-relative slash paths, no symlinks; the core's own
  ignore rules for `.git`/hidden dirs incl. `.room/`, `node_modules`, `.venv`,
  `target`, `dist`/`build`; capped at `protocol.MaxSessionFiles` = 2000) — and
  `sessionFileBytes` — `GET /core/v1/workspaces/{ws}/files/{path}` (the route
  `writeFile` PUTs) → raw bytes with sniffed `Content-Type` and
  `Content-Length`; `400 bad_request` for an absolute/`..` path,
  `404 file_not_found` for a missing file, a directory or anything resolving
  outside the workspace (symlinks included), `413 file_too_large` over the
  core's cap. New error codes `file_not_found`, `file_too_large`.
- **Client.** `protocol.Client` implements `core.SessionFiles`; `Supports`
  reports both capabilities from discovery, so the bridge's existing
  `sessionFiles` resolution publishes protocol-core deliverables, binaries
  included, with no bridge change. `ReadSessionFile` requires
  `sessionFileBytes` (capability_missing otherwise) and returns the body
  bytes verbatim.
- **Conformance.** When offered: the listing must succeed with clean relative
  paths; the first listed file must read back at its listed size; a path
  escaping the workspace must be refused. `CheckLive` fails a daemon that
  advertises one of the pair without the other.
- **Tests.** `core/protocol` (wire mapping, exact bytes, escaped path, absent
  capability), `core/conformance` (pass, size mismatch, unpaired
  capability), bridge `TestPublishDeliverablesFromProtocolCore` (real
  protocol client → PNG blob + markdown inline). The private adapter core
  implements both (os.Root-confined walk and open, sandboxed workspaces
  included) and was verified end to end from its release binary with the
  public client: `node_modules` skipped, PNG bytes identical, `..` refused,
  `CheckLive` passing.
- **Docs.** `docs/architecture/core-providers.md` (adapter daemon section),
  `docs/prd/2026-09-20-core-protocol-v1.md` (endpoint table).
