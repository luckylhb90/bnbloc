# Previewer components

A previewer is an installable viewer for artifact files: a static web bundle
that renders one file version inside the preview workbench, in a sandboxed
opaque-origin frame, next to the built-in viewers. It is a Component like a
core or a plugin — signed, sha256-pinned, installed by a super-admin, rolled
out (`auto | optin | off`) and tenant-scoped — but nothing runs server-side
and it never gets network access.

Design record: [`../prd/2026-10-09-preview-workbench-and-previewer-components.md`](../prd/2026-10-09-preview-workbench-and-previewer-components.md)
(section 4 and "Contracts").

## Package

```json
{
  "kind": "bnbloc.previewer.package/v1",
  "contract": "bnbloc.previewer.contract/v1",
  "runtime": "bundle",
  "name": "sprite-sheet",
  "version": "1.0.0",
  "publisher": "example",
  "description": "Sprite sheets and texture atlases",
  "artifacts": { "bundle": { "url": "<gateway artifact URL>", "sha256": "<hex>" } },
  "previewer": {
    "entry": "index.html",
    "match": { "mimeTypes": ["image/png", "image/*"], "extensions": [".atlas.json"] },
    "rank": 50,
    "capabilities": ["thumbnail", "liveState", "toolbar"],
    "toolbar": [
      { "id": "zoom", "icon": "zoom-in", "label": "Zoom", "kind": "zoom" },
      { "id": "grid", "icon": "grid", "label": "Grid", "kind": "toggle" },
      { "id": "fit", "icon": "fit", "label": "Fit to view", "kind": "button" }
    ]
  }
}
```

Validation (`core/pkgmeta/previewer_validation.go`, enforced at install):

- exactly one artifact, `bundle`; no endpoint, sidecar, manifest, hooks,
  protocol, top-level capabilities, local settings, guest, or
  `permissions.network`;
- `entry`: a bundle-relative `.html` path, segments `[A-Za-z0-9._-]`, no `.`
  / `..`, at most 200 characters;
- `match`: lower-case `type/subtype` or `type/*` MIME types (never `*/*`, no
  parameters) and `.ext` extensions (up to three parts, e.g. `.atlas.json`);
  at least one entry, at most 32 per list, no duplicates;
- `rank`: 1–1000, lower wins among equally specific matches (built-ins use
  10). Specificity, highest first: multi-part extension (`.atlas.json`
  matching the end of the name — it beats the JSON viewer's exact
  `application/json`), exact MIME, plain extension, kind, MIME wildcard;
- `capabilities` ⊆ `thumbnail`, `liveState`, `toolbar`, `selection`, `units`
  (`edit` is reserved); a host ignores a capability it predates (it never
  drops the previewer for one);
- `toolbar`: at most 20 items, unique ids `[a-z][a-z0-9-]{0,31}`, an icon
  NAME from the code-owned list, a 1–40 character label, kind
  `button | toggle | zoom`; a non-empty toolbar requires the `toolbar`
  capability;
- no prompts: a file's suggested requests are prompt-pack prompts scoped to
  the files they are for (`files`; `docs/prd/2026-10-14-file-prompts-and-compare.md`).
  The old `suggestions` field is refused by the strict schema. The built-in
  packs are per domain (`file-prompts` for Office, PDF, Markdown, tables,
  images and media; `code-prompts`; `diagram-prompts`); for a selection the
  prompts scoped to the file's type *and* the selection's kind replace the
  generic ones (`docs/prd/2026-10-16-ask-ai-every-file.md`). A previewer for a
  new type gets suggestions by someone publishing a prompt pack for it.
- pointing is one engine, `web/previewer-sdk/src/pointing.ts`: the same code
  runs in a previewer frame (reporting through `sendSelection`) and in the
  app's built-in viewers (reporting to the tab). A viewer lists its parts as
  data (`PartRule`: a selector, a noun, a kind) and says where selected text is.

The vocabularies (capabilities, toolbar kinds and icons, limits) are defined
once in `web/previewer-sdk/src/protocol.ts`; the Go validator mirrors them and
`TestPreviewerVocabulariesMatchFrameProtocol` fails when they drift.

### Bundle archive

An uncompressed POSIX ustar `.tar` of regular files (directory entries are
allowed and ignored; links, devices and PAX/GNU extension headers are
refused), at most 16 MiB of file data and 512 files, every path valid as
above, containing `entry`. A single-file previewer is a tar with one `.html`.
The same strict format is read by the gateway
(`internal/tarbundle`) and the desktop shell
(`apps/desktop/src-tauri/src/internal_ext/previewer_bundle.rs`), so a bundle
that installs is readable by every client.

Relative asset URLs (`<script src="main.js">`, `url(fonts/x.woff2)`) work:
each served file lives under the same base path. Classic and module scripts
both load (bundle files carry `Access-Control-Allow-Origin: *`, which the
opaque-origin frame's CORS-mode loads need).

## Install and distribution

1. The super-admin uploads the archive through `POST /v1/platform/artifacts`
   (the gateway's content-addressed store; it returns the sha256 it computed).
2. The package pins that URL + sha256 and is signed and published through
   `POST /v1/platform/components` (CLI: `bnbloc previewers publish`, or the
   platform console's Components page — paste the package, upload the
   bundle in the artifact step, publish).
3. The install pipeline (`internal/previewerruntime`) verifies the
   signature and package, then the gateway probe reads the archive back
   from the store, checks its sha256, parses it, and requires `entry`.
   Nothing is activated: a previewer has no live driver.

Members see installed previewers in the member catalog,
`GET /v1/components[?kind=previewer]`, under the same rollout + tenant gate
as every kind. An installed entry carries:

```json
"previewer": {
  "entry": "index.html", "match": {...}, "rank": 50, "capabilities": [...], "toolbar": [...],
  "bundleUrl": "https://gw.example.com/v1/artifacts/sha256/<hex>",
  "bundleSha256": "<hex>",
  "frameUrl": "/v1/previewers/sprite-sheet/1.0.0/<ticket>/",
  "frameExpiresAtMs": 1791546426000
}
```

`bundleUrl` is entitlement-gated like every component artifact
(`GET /v1/artifacts/{key}`); `frameUrl` is the browser frame base.

## Serving the bundle

**Browser.** `GET /v1/previewers/{name}/{version}/{ticket}/{path…}`
(`services/gateway/previewers.go`). An iframe `src` cannot carry the session
bearer, and a sandboxed frame's subresource loads carry no usable cookie, so
the path carries a frame ticket: a 1-hour token signed with the session key,
of kind `previewer-frame`, bound to the subject, tenant and `name@version`.
Relative asset loads inherit it. Every request re-checks the ticket, the
rollout + tenant gate, and that `version` is the installed version (an
upgrade retires older frames), then serves the file from the verified
archive (fetched from the blob store, sha256-checked, cached by hash) with:

```
Content-Security-Policy: default-src 'none'; script-src 'self' <bundle base>;
  style-src 'self' <bundle base> 'unsafe-inline'; img-src blob: data:;
  media-src blob: data:; font-src 'self' <bundle base> data:;
  connect-src 'none'; frame-ancestors 'self'
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Access-Control-Allow-Origin: *
Cache-Control: private, no-cache
```

`<bundle base>` is `<public origin>/v1/previewers/<name>/<version>/`, named
besides `'self'` because engines differ on what `'self'` means inside an
opaque-origin document. Bad tickets, gated components, path traversal and
unknown files are all 404. The ticket only reads this bundle's files for its
subject; leaking it to the previewer (which can read its own URL) grants
nothing new.

Deployments behind a proxy that adds `X-Frame-Options: DENY` everywhere
still frame previewers: browsers ignore `X-Frame-Options` when the response
carries CSP `frame-ancestors`. The gateway's own artifact runner page
(`/artifact-runner.html`, HTML/app previews) sends `frame-ancestors 'self'`
for the same reason, and the sample `deploy/Caddyfile` / nginx config use
`X-Frame-Options: SAMEORIGIN` since the app frames its own pages.

**Desktop.** `bnbloc-previewer://localhost/{name}/{version}/{path…}`
(Windows: `http://bnbloc-previewer.localhost/…`), served by the shell
(`apps/desktop/src-tauri/src/previewer_scheme.rs`). The shell resolves the
previewer from the member catalog with its own session, downloads
`bundleUrl` (gateway origin only), refuses a sha256 mismatch, caches the
verified archive (memory + app cache dir, re-hashed on load), and serves
files with the same CSP (bundle base = the scheme origin, `frame-ancestors`
= the app origin). The app CSP's `frame-src` admits only this scheme and the
artifact runner's. A reconcile drops cached bundles of previewers that left
the catalog. Statuses: 404 (bad path / not installed / unknown file), 405,
502 (bundle refused), 503 (no session / catalog unreachable — retry).

## Frame protocol

The host loads `<frame base><entry>#host=<host origin>` into
`<iframe sandbox="allow-scripts">` (never `allow-same-origin`). All messages
are `postMessage` objects with a `type`:

| direction | message | notes |
|---|---|---|
| frame → host | `preview.ready` | once, on window load (after the bundle registered its handlers) |
| host → frame | `preview.init {protocol: 1, file: {name, mimeType, sizeBytes, version}, theme, viewport: {w, h}}` | answer to ready |
| host → frame | `preview.file {bytes: ArrayBuffer}` | transferred, right after init |
| frame → host | `preview.toolbar.set {items}` | replaces the tab's toolbar; items follow the package toolbar rules plus state: `active` (toggle), `value` (zoom factor 0.01–64), `disabled` |
| host → frame | `preview.toolbar {id, value}` | a gesture: `null` (button), boolean (toggle), number (zoom) |
| frame → host | `preview.state.send {state}` | JSON ≤ 16 KiB, relayed to the room as `artifact_state` (needs `liveState`) |
| host → frame | `preview.state {state, actor}` | room live state (join snapshot and remote changes) |
| frame → host | `preview.thumbnail {png: ArrayBuffer}` | PNG ≤ 512 KiB; taken only when the previewer declares `thumbnail`, and kept where the host says (`onCover`): in a room via `PUT …/versions/{n}/thumbnail` when the version has none, in a work's detail as the work version's stored cover (`docs/prd/2026-10-19-work-covers.md` §2.3) |
| frame → host | `preview.error {message}` | ≤ 500 characters, shown over the frame |
| frame → host | `preview.title {text}` | ≤ 120 characters, the frame's accessible title |
| host → frame | `preview.units.request {id, file, bytes}` | Compare reads a version into units, in a hidden frame (needs `units`) |
| frame → host | `preview.units {id, units, truncated}` or `{id, error}` | ordered units `{key?, label, text}`: at most 5,000, key ≤ 200, label ≤ 120, text ≤ 4,000 characters, 2 MiB in all; `key` is a stable identity across versions when the format has one (a slide id, a cell address). The host registers the previewer as the comparer of its files |
| frame → host | `preview.selection {selection}` | the part the member points at, `{label ≤ 120, detail ≤ 2000}` or `null` to clear (needs `selection`); the Ask AI panel shows the label as a removable chip and sends both with the request, the detail fenced as the file's data |
| host → frame | `preview.resize {w, h}` | viewport changes |

Validation on both sides uses the same module
(`web/previewer-sdk/src/protocol.ts`): the host accepts only messages whose
origin is `"null"` and whose source is that frame's window
(`web/src/lib/previewerFrame.ts`); the SDK accepts only messages from
`window.parent` with the origin named in `#host=`. The frame posts with
target `"*"` (a custom-scheme app origin is not reliably matchable as a
target; the parent is pinned by `frame-ancestors`, and nothing the frame
sends is secret).

## Security model

- **Isolation.** Opaque origin: no cookies, storage, app DOM, same-origin
  API calls, or (desktop) Tauri IPC — the shell injects that into the main
  frame only.
- **No network.** CSP `default-src 'none'` and `connect-src 'none'`; scripts,
  styles and fonts only from the bundle; images and media only from
  `blob:`/`data:` (the transferred file bytes). Frame navigation is bounded
  by the app's `frame-src`.
- **Integrity.** The archive is pinned by sha256 in the signed package and
  re-verified by every server (install probe, frame route) and client
  (desktop download, disk cache).
- **Data only.** The previewer drives host chrome with data: toolbar items
  by icon name from a code-owned list, a bounded title, an error string,
  live state into the room's existing state channel, and a PNG thumbnail.
- **Gate.** Rollout + tenant scope apply to the catalog, the frame route
  (per request), the desktop scheme (via the catalog) and the artifact
  download.

## Building a previewer

The SDK (`web/previewer-sdk`, `@bnbloc/previewer-sdk`, no dependencies)
implements the frame side:

```ts
import { onInit, onFile, onToolbar, onState, onResize, setToolbar, sendState,
  provideThumbnail, setTitle, sendSelection, onUnits, reportError } from "@bnbloc/previewer-sdk";

onFile(async (bytes, file) => {
  const url = URL.createObjectURL(new Blob([bytes], { type: file.mimeType }));
  // render …
  setTitle(`${file.name}`);
  await provideThumbnail(await canvasToPngBlob());
});
onToolbar((id, value) => { /* apply the gesture, then setToolbar(updatedItems) */ });
onState((state, actor) => { /* apply another member's live state */ });
// The member clicked frame 7 ("selection" capability); sendSelection(null) clears.
sendSelection({ label: "Frame 7", detail: "walk_cycle_07: 64×64 at 448,0" });
// Compare ("units" capability): a version's units; the SDK fits them to the limits.
onUnits(async (bytes) => frames(bytes).map((f) => ({ key: f.name, label: f.name, text: f.rect })));
```

It also exports `@bnbloc/previewer-sdk/compare` (`diffUnits`, the alignment
the host's Compare uses), so a previewer tests its units the way the host will
read them. A previewer built outside this repository depends on the SDK from
it, pinned to a commit (pnpm:
`"@bnbloc/previewer-sdk": "github:luckylhb90/bnbloc#<commit>&path:/web/previewer-sdk"`).

Bundle it into a classic script (or modules) with relative URLs and an
`index.html` without inline script. The bnbloc previewers live in their own
repository, [`luckylhb90/bnbloc-previewers`](https://github.com/luckylhb90/bnbloc-previewers)
(docs/prd/2026-10-18-components-in-own-repos.md): the reference `sprite-sheet`
(images and sprite-sheet / texture-atlas JSON with zoom, fit, grid,
checkerboard, frame boundaries, live layer state and a thumbnail), the Office
previewers `pptx`, `docx` and `xlsx` (`docs/prd/2026-10-14-office-previewers.md`)
and the diagram previewers `mermaid` and `drawio`
(`docs/prd/2026-10-16-diagram-previewers.md`). Their shared build makes the
deterministic archive, the package placeholder and `NOTICES.txt` (the license
files of every third-party package esbuild inlined; a bundled package without
one fails the build):

```sh
pnpm install && node sprite-sheet/build.mjs     # in a bnbloc-previewers checkout
# dist/site/ (the bundle files), dist/bundle.tar, dist/bundle.tar.sha256, dist/package.json
```

## Publishing

```sh
bnbloc previewers pack <dir> --out bundle.tar           # deterministic archive + sha256
bnbloc previewers publish --package dist/package.json --bundle dist/bundle.tar --rollout optin
# or: --dir dist/site  (packs for you); --unsigned for a dev gateway (BNBLOC_EXT_ALLOW_UNSIGNED)
```

`publish` validates the package and the archive (entry present) before
anything is uploaded, uploads the archive, pins its URL + sha256 into
`artifacts.bundle`, signs with the publisher key (`--seckey`, default
`~/.bnbloc/component-publisher.key`), and installs. Manage it afterwards with
the component routes and `?kind=previewer` (rollout, tenants, uninstall), or
in the console.

Without the CLI: upload the archive via `POST /v1/platform/artifacts`, put
the returned URL + sha256 into `artifacts.bundle`, sign the package body
(`bnbloc keys gen`, minisign-compatible), and publish with
`bnbloc cores install --package <file> --signature <file>` or the console.

## Web host

Installed previewers join the one viewer registry
(`web/src/views/artifact/registry.ts`) as `source: "component"` viewers from
the member catalog (`remotePreviewers.ts`, re-read at sign-in, tenant switch
and every 10 minutes; a previewer that leaves the catalog is unregistered).
`RemotePreviewer.tsx` hosts the frame and speaks the protocol; a frame
ticket near expiry is refreshed by re-reading the catalog before a new frame
mounts. "Open with…" (`OpenWithMenu.tsx`) lists every viewer that claims
the file and remembers the member's choice per MIME type / extension
(`lib/viewerPreference.ts`, localStorage).
