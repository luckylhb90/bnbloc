# Public share links and live web-app previews (2026-10-09)

Status: implemented (2026-10-09); awaiting deploy + live verification

Follow-up of `2026-10-09-preview-workbench-and-previewer-components.md`
(phase 4 + "external share links"). Product decision (2026-10-09): do both,
and build on the S3 blob store we already run instead of a relay tunnel.

## Intent

1. **Share links.** A room member creates an expiring, revocable link to one
   deliverable (a pinned version, or "always the latest"). Anyone with the
   link sees it without signing in, rendered by the same viewers as the
   workbench, and can download it. Org admins can switch public links off
   for the whole organization and see/revoke every active link.
2. **Live web-app previews ("sites").** When an agent builds a static web
   app (plain HTML/CSS/JS, or a build output such as `dist/`), the bridge
   publishes the whole directory as one `site` artifact version: a tar
   snapshot in the blob store (S3). The workbench runs it in a sandboxed
   frame. It updates after every agent turn, which is the "live" part, and
   it can be shared through the same link mechanism.
3. **Direct downloads from S3.** With direct downloads switched on, large
   downloads (deliverables, shared files, component artifacts the desktop
   fetches) go straight to S3 through short-lived presigned URLs instead of
   streaming through the gateway.

Out of scope: dynamic dev servers or app backends (needs a tunnel; revisit if
static snapshots prove insufficient), custom domains, password-protected
links.

## Acceptance

- Member: Share → choose "this version" / "always latest" and an expiry
  (1, 7, 30, 90 days) → link copied. The shares list shows views, expiry,
  revoke. A revoked, expired or org-disabled link answers 404, the same as
  an unknown one.
- An outsider opening the link sees title, "shared by", expiry, the rendered
  file (image, PDF, markdown, code, JSON, CSV, media, HTML app, site) and a
  Download button; nothing else of the room (no room name, members or other
  files).
- An agent turn that writes `index.html` + assets (or runs a build into
  `dist/`) produces a "Web app" card; opening it runs the app in a tab with
  reload, device widths, open-in-new-tab and share. The next turn's changes
  show up without a manual refresh.
- With `BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS=true`, downloads answer 302
  to a presigned S3 URL; with it off (the default) everything streams as
  today.

## Contracts

### Data (MySQL; both schema files + `migrations.go` + `migrations.lock`)

- Migration `2026_10_artifact_shares`: table `artifact_shares`:
  `share_id VARCHAR(32) PK`, `token_hash CHAR(64) NOT NULL UNIQUE`
  (sha256 hex of the secret), `tenant_id`, `room_id`, `artifact_id`,
  `version INT NOT NULL` (0 = always the latest), `created_by`,
  `created_at_ms`, `expires_at_ms`, `revoked_at_ms NULL`, `revoked_by NULL`,
  `view_count BIGINT NOT NULL DEFAULT 0`, `last_viewed_at_ms NULL`; index
  `(tenant_id, room_id, artifact_id)`. Room export does not carry shares;
  deleting an artifact or a room makes its links resolve 404 (rows can stay).
- Migration `2026_10_tenant_public_shares`: `tenants.public_shares_disabled
  TINYINT(1) NOT NULL DEFAULT 0`.
- Token: `bsh_<shareId>_<secret>` (secret 32 random bytes, base64url); only
  the hash is stored; verification is constant-time (same pattern as API
  tokens). Shown once, at create.

### Room service (member routes, behind the gateway as today)

- `POST /v1/rooms/{roomId}/artifacts/{artifactId}/shares`
  `{version?: int (0 or absent = latest), expiresInHours?: 1..2160 (default 168)}`
  → 201 `{share: ShareView, token}`. Room member only. `version > 0` must
  exist. Tenant disabled → 403 `{"error":"public share links are disabled for this organization"}`.
- `GET …/artifacts/{artifactId}/shares` → `{shares: ShareView[]}` (newest
  first; revoked/expired included for 30 days).
- `DELETE …/artifacts/{artifactId}/shares/{shareId}` → 204; the creator, the
  room owner/admins, or an org admin.
- `ShareView = {shareId, artifactId, version, createdBy, createdAtMs,
  expiresAtMs, revokedAtMs?, revokedBy?, viewCount, lastViewedAtMs?, active}`.
- Org admin: `GET /v1/tenant/shares?active=1` → `{shares: (ShareView &
  {roomId, artifactTitle})[]}`; revoke through the DELETE above (an org admin
  passes the membership check for this route only).
- Internal, svcauth-signed, gateway-only (`X-Bnbloc-User` = the gateway
  daemon subject): `POST /internal/v1/shares/resolve`
  `{token, countView: bool}` → 200 `{tenantId, roomId, share: ShareView,
  artifact: {artifactId, kind, title, fileName}, version: {version,
  storage: "inline"|"blob", mimeType, sizeBytes, contentHash, blobKey?,
  content? (inline only), summary?, createdAtMs}, sharedBy: {userId,
  displayName}}` for an active link (latest version resolved at call time),
  else 404 for every other case (unknown, bad secret, expired, revoked,
  tenant disabled, artifact/room gone).

### Tenant policy (shareagent; clean cutover of the sandbox-policy read)

- `GET /v1/my/sandbox-policy` becomes `GET /v1/my/tenant-policy[?tenant=]`
  → `{tenantId, requireSandboxForBorrow, publicSharesDisabled}`. The
  bridge's policy fetch and the web move to it; the old route is deleted.
- `PUT /v1/tenant/settings` accepts `publicSharesDisabled` (org admin).

### Blob store direct downloads (realtime owns the store)

- `blobstore.Presigner` (optional interface, implemented by `S3Store`):
  `PresignGet(ctx, key, ttl, PresignOptions{ContentType,
  ContentDisposition}) (url string, err error)`. SigV4 query signing reuses
  the existing signer; temporary credentials add `X-Amz-Security-Token`.
- Env (realtime only): `BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS` (bool,
  default false) and `BNBLOC_ATTACHMENTS_S3_PUBLIC_ENDPOINT` (optional base
  URL used as the host of presigned URLs when the S3 endpoint realtime uses
  is not the one clients can reach). All deploy surfaces + env docs.
- `POST /internal/v1/blobs/presign {key, fileName?, contentType?}` → 200
  `{url, expiresAtMs}` (TTL 5 min), or 404 `{"error":"direct downloads
  disabled"}` (disk store, or the switch off) → the caller streams.
- Opt-in redirects (`?download=1` on a deliverable/share, `?direct=1` on
  `/v1/artifacts/{key}`) answer 302 to the presigned URL when available. A
  plain `fetch()` by a viewer never gets a cross-origin redirect (S3 CORS is
  not assumed).

### Gateway

- Public, unauthenticated, per-client-IP rate-limited (reads ~10/s, burst
  40; the existing guess limiter for token misses), never a session:
  - `GET /v1/public/shares/{token}` → `{artifact: {kind, title, fileName},
    version: {version, mimeType, sizeBytes, createdAtMs, summary?}, sharedBy:
    {displayName}, expiresAtMs, followsLatest, siteUrl?}` (counts a view;
    `Cache-Control: no-store`).
  - `GET /v1/public/shares/{token}/content[?download=1]` → the bytes
    (`Content-Disposition` inline/attachment with the file name,
    `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox`).
  - `GET /v1/public/shares/{token}/site/{path...}` → a file of a `site`
    version (rules below).
- Members: `POST /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/site-ticket`
  → `{siteUrl: "/v1/sites/<ticket>/", expiresAtMs}` (membership checked; ticket = signed
  claims kind `site-frame`, ID `<roomId>/<artifactId>/<version>`, tenant, 12 h — a running app keeps its URL for lazy loads; membership is re-checked every 30 s).
  `GET /v1/sites/{ticket}/{path...}` serves the files; the room-level read
  is re-checked when the bundle is not cached for this subject (bounded
  authorization cache, ≤ 30 s).
- **Site file rules** (one implementation for the member and public routes):
  bundle = `internal/tarbundle` with `tarbundle.SiteLimits`, parsed from the
  version blob and cached by content hash (LRU, 256 MiB). `""` or a
  directory → its `index.html`; an extensionless miss → root `index.html`
  (SPA fallback); else 404. Headers: `Content-Type` from
  `tarbundle.ContentType` (extend the map for common web types),
  `Content-Security-Policy: sandbox allow-scripts allow-forms allow-popups
  allow-modals allow-downloads; frame-ancestors *` (opaque origin even when
  opened top-level: no gateway cookies/storage; network allowed so apps can
  use public APIs/CDNs), `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`, `Access-Control-Allow-Origin: *`,
  `Cache-Control: private, max-age=300`. **Root-relative URLs** (`/assets/x`) in served
  `.html` (src, href, action, poster, srcset, `<meta http-equiv=refresh>`)
  and `.css` (`url(/…)`, `@import "/…"`) are rewritten to the site base so
  common build outputs (Vite/CRA with base `/`) work under a path; `//` and
  absolute URLs are untouched. Runtime JS that builds root-relative URLs is
  not rewritten (documented limitation).

### Bridge (site snapshots)

- Kind `site`; version storage blob, MIME
  `application/vnd.bnbloc.site+tar`, `fileName` = the root dir name (or
  "site"), title = `<title>` of `index.html`, else the dir name.
- Detection per publish pass, over the session file listing: a **site root**
  is a directory holding `index.html` that is not inside a dependency dir
  (`node_modules`, `vendor`, hidden). A root whose `index.html` loads a
  module that needs a build (`.ts/.tsx/.jsx/.vue/.svelte` script src) is not
  a site (its build output is). A root nested in another accepted root is
  part of the outer one. At most 3 sites per pass.
- Content: every listed file under the root except dependency/hidden dirs;
  paths must be valid bundle paths (others are skipped with a log line);
  caps `tarbundle.SiteLimits` and the room's artifact max bytes (over → not
  published, logged). Packed with `tarbundle.PackFiles` (deterministic), so
  an unchanged site re-publishes as `unchanged`.
- Folding: web-asset files under an accepted root (html, css, js/mjs, wasm,
  json, map, images, fonts, media, ico, webmanifest) are not published
  individually; other files (md, py, …) still are.
- Requires the core listing to include build outputs: the sidecar core daemon stops
  skipping `dist`, `build` and `out` (the bridge's own filter keeps them out
  of individual deliverables).

### Web + desktop

- Share dialog (workbench toolbar, file card menu): version choice, expiry,
  copy link `${origin}/#/share/<token>`; list with views/expiry/revoke.
  Hidden when `publicSharesDisabled`.
- Public page `#/share/<token>`: renders without sign-in, calls only
  `/v1/public/...`, reuses the viewer registry (bytes from the public content
  route; sites in a sandboxed iframe of `siteUrl`).
- Site tab: sandboxed iframe (`allow-scripts allow-forms allow-popups
  allow-modals allow-downloads`, never `allow-same-origin`) of the site URL;
  toolbar reload, device widths (responsive / 390 / 820 / 1280), open in new
  tab, share; follows the latest version live (auto-reload on
  `artifact_updated` unless pinned). File card: "Web app".
- Organization page: "Allow public share links" toggle; "Public links"
  list (active links, room, artifact, creator, expiry, views, revoke).
- Desktop: a `bnbloc-site://localhost/v1/sites/{ticket}/{path}` scheme that
  proxies to the gateway's `/v1/sites/...` (ticket in the path, no session),
  added to the app CSP `frame-src`; the component artifact downloader asks
  for `?direct=1`.

## Implementation notes

### Shares, tenant policy, store (backend)

- Store: `ArtifactShareStore` (`internal/store/artifact_shares.go`, MySQL in
  `mysql_artifact_shares.go`) is part of `ArtifactStore`, so the org-admin
  list joins `artifacts` for the title and drops links of artifacts that are
  gone. Every method is tenant-scoped except `ResolveArtifactShare` (share
  ids are globally unique; the caller verifies the secret, then works in the
  row's tenant). Revoke keeps the first revocation. `DeleteRoom` also clears
  the room's `artifact_shares` rows. Tenant switches: `SetSandboxPolicy` is
  replaced by `SetTenantPolicy(ctx, id, TenantPolicyUpdate{...})` (one
  `COALESCE` update; unset fields keep their value).
- Token: `internal/secrettoken` (`Format{Prefix, IDLen}.New/Parse`,
  `HashSecret`, constant-time `Verify`), extracted from the gateway's API
  tokens, which now use it too. Shares use `bsh_` with a 32-hex id.
- Room service (`services/room/artifact_shares.go`): the member routes as in
  the contract. Create checks the tenant switch (a tenant with no registry row
  has the defaults); a bad `version`/`expiresInHours` is 400. Lists keep
  links that stopped working within 30 days. `active` in a ShareView is the
  link's own state; the org switch is reported by `/v1/my/tenant-policy`.
  `DELETE` is allowed for the creator, a room owner/admin, or an org admin
  (`tenant_members.role = org_admin` in the room's tenant). The gateway lets
  a `manage_org` session past its room-scope check for that one route
  (`apiPath.isArtifactShareRevoke`), and the room service re-checks the role.
  A super-admin who is not an org member of that tenant is refused.
  `GET /v1/tenant/shares[?active=1]` is served by the room service (gateway
  routes it there, `manage_org` gated like every `/v1/tenant/*` path), up to
  500 rows, newest first.
- `POST /internal/v1/shares/resolve` is `svcauth.RequireSigned` and answers
  only `X-Bnbloc-User = svcauth.ServiceSubject("gateway")` (`svc:gateway`,
  the gateway's blob-read identity); others get 403. Every inactive case is
  404. `countView` failures are logged and never fail the read.
  `sharedBy.displayName` is the user id (bnbloc keeps no profile directory).
  An inline version without a stored MIME type reports the artifact's inline
  type, as the content route does.
- Kind `site`: accepted by `validArtifactKind` (create and import). Its
  versions must be blobs of `tarbundle.SiteMIME`, and only a site may carry
  that type (`siteTypeMismatch`, used by blob publish and handoff import). An
  undeclared type on a site upload is typed as `SiteMIME`. Inline
  content/versions on a site are 400. The content route serves the tar as
  `attachment` with nosniff and the sandbox CSP, never as HTML.
- Tenant policy: shareagent `GET /v1/my/tenant-policy[?tenant=]` replaces
  `/v1/my/sandbox-policy` (deleted). `PUT/GET /v1/tenant/settings` carry
  `publicSharesDisabled`. The bridge's policy fetch and the gateway route
  table moved to it. Migrations: `2026_10_artifact_shares` and
  `2026_10_tenant_public_shares` (locked).

### Gateway: public routes, sites, direct downloads

- **Presigning.** `blobstore.Presigner` / `S3Store.PresignGet`
  (`internal/blobstore/s3sig.go` `presignV4`, checked against the AWS
  query-auth example vector): host-only signed, `UNSIGNED-PAYLOAD`,
  `X-Amz-Security-Token` for temporary credentials, `response-content-type`
  / `response-content-disposition` signed into the URL; the object URL
  (path-style or virtual-host, key prefix) is built once
  (`objectURL`) for both requests and presigns, on
  `BNBLOC_ATTACHMENTS_S3_PUBLIC_ENDPOINT` when set. Realtime:
  `POST /internal/v1/blobs/presign` (TTL 5 min, attachment disposition with
  the file name) behind `internal()`; 404 `direct downloads disabled` unless
  `BNBLOC_ATTACHMENTS_S3_DIRECT_DOWNLOADS` is on and the store presigns.
  `HandoffClient.PresignBlob` maps that 404 to
  `realtime.ErrDirectDownloadsDisabled` (= stream).
- **One blob client.** The gateway reads realtime's store only through
  `HandoffClient` (`Service.blobs()`, subject `service:gateway`, also the
  share-resolve subject): previewer bundles, site snapshots, public content,
  `/v1/artifacts/{key}` and the install probe (the hand-rolled signed GETs
  are gone).
- **Redirects.** `?direct=1` on `/v1/artifacts/{key}` (after the
  entitlement check), `?download=1` on a public share's `/content` (blob
  versions) and on the member `…/versions/{v}/content` (room service,
  `handleArtifactContent`) answer 302 + `Cache-Control: no-store` +
  `Referrer-Policy: no-referrer`; with direct downloads off they stream, as
  an attachment. The member download route also accepts the session as
  `access_token` (only with `download=1`), so a plain `<a href>` works.
- **Public routes** (`services/gateway/public_shares.go`): token sanity
  check (`bsh_` prefix, charset, ≤ 256) before any backend call; per-IP read
  limiter (10/s, burst 40) plus the guess limiter (`share-ip:<ip>`, spent
  only by a miss, checked without consuming via `rateLimiter.blocked`);
  limiters are one `lazyLimiter` helper. Metadata counts a view; content and
  site reads do not; the site route reuses a resolution for 10 s (bounded
  cache, keyed by the token's sha256), so a revoked link stops serving site
  files within 10 s. `siteUrl` = `/v1/public/shares/<token>/site/` for a
  `site` artifact whose version is a site snapshot.
- **Sites** (`services/gateway/sites.go`, `site_rewrite.go`): ticket kind
  `auth.KindSiteFrame`, ID `<roomId>/<artifactId>/<version>`, tenant = the
  room's tenant. The room-level read is the room service's version GET as
  the ticket subject; a positive result is cached 30 s per subject+version
  (the ticket mint seeds it). Bundles load by `blobKey` and are cached by
  content hash in `tarbundle.Cache` (LRU, byte-bounded, one shared load per
  digest), the same helper now backing the previewer cache (64 MiB; sites
  256 MiB). A directory path without its slash, and a site base without its
  slash, redirect to the slash form. HTML rewriting uses the
  `golang.org/x/net/html` tokenizer: untouched tokens are copied byte for
  byte, edited tags re-serialized; URL attributes (`src href action
  formaction poster data background xlink:href manifest`), `srcset` /
  `imagesrcset`, refresh `<meta>`, `style` attributes and `<style>` bodies.
  `tarbundle.ContentType` learned common web types (htm, cjs, ico, avif,
  webmanifest, media, fonts, pdf, …). `routeLabel` knows `public`, `shares`,
  `resolve`, `presign`, `site`, `sites`, `site-ticket`.

### Bridge site snapshots + core listing

- `services/bridge/sites.go`, called from `publishDeliverables` (only when
  the core's reads are binary-safe; otherwise no detection and no folding,
  files publish individually as before). One change predicate (the
  `fileStamp` diff, cold start = this turn's writes) feeds both the
  individual deliverables and the sites; removed paths also mark their root
  changed. Only changed roots are evaluated, shallowest first; a root nested
  in an accepted one is skipped; `index.html` is read once (detection, title
  and bundle).
- The bundle is every listed file under the root minus hidden entries
  (files and dirs) and dependency dirs; invalid bundle paths are skipped with
  one `bnbloc.bridge.site_paths_skipped` log line. Over
  `tarbundle.SiteLimits` (checked on the listing before any read) or the
  room's `maxBytes` (listing total and final archive) → not published,
  `bnbloc.bridge.site_too_large`. A file that vanishes mid-read skips the
  site for that pass.
- Version: `deliverable{rel: root+"/"}` through `publishArtifactNow` (the
  trailing slash keeps the stable id `artifactIDForFile(ws, sid, root+"/")`
  disjoint from file ids), kind `store.ArtifactKindSite`, MIME
  `tarbundle.SiteMIME` (new constant, the one literal for the format),
  multipart blob publish with `sourceJobId`/`sourceSeq`.
- Per-pass cap: the 3 most recently written roots publish; a deferred root's
  `index.html` stamp is dropped from the snapshot so the next pass sees it as
  changed. Accepted roots (published, deferred or over the caps) fold their
  web assets out of the individual list.
- The bridge's ignore set is split into `dependencyDirs` (never a
  deliverable, site or site content) and `buildOutputDirs` (`dist`, `build`,
  `out`: not individual deliverables, but site detection sees them).
- the sidecar core daemon (`bee51cf`): the shared walker (`listWorkspaceFiles`, both wire
  dialects) no longer prunes `dist`, `build`, `out`; dist binaries rebuilt
  (not published as a component).
- Tests: `services/bridge/sites_test.go` (plain root, Vite source vs
  `dist`, nested roots, room/file caps, change detection + deterministic
  re-pack + removal, invalid paths, per-pass deferral, text-only core).

### Web + desktop

- **API.** `lib/shares.ts` (wire shapes + pure label/request helpers),
  `lib/sharesApi.ts` (member routes through the authenticated `req`; the
  public routes as plain `fetch` with `credentials: "omit"` — never a
  session). Clean cutover: `api.mySandboxPolicy` → `api.myTenantPolicy`
  (`/v1/my/tenant-policy`), read through `lib/tenantPolicy.ts` (cached per
  session tenant, 5 min; invalidated when an admin flips a policy switch);
  the settings sandbox section uses it too. `updateTenantSettings` takes
  `publicSharesDisabled`; `TenantSettings` (the GET) carries it.
- **One viewer path.** Viewers never fetch: `ArtifactBody` gets the bytes (or,
  for `input: "site"`, a `SiteSource` that resolves a fresh `siteUrl`) from
  its host. The workbench supplies member bytes + `site-ticket`; the public
  page supplies the public content route + the link's `siteUrl`.
  `views/artifact/fileInfo.ts` is the one facts → `ArtifactFileInfo`
  derivation both use. Sites: `SITE_MIME`, kind `site` → category `web`,
  label "Web app" (globe), named by title, downloaded as `<name>.tar`.
- **Site viewer** (`views/artifact/SiteViewer.tsx`, built-in `core.site`):
  iframe `sandbox="allow-scripts allow-forms allow-popups allow-modals
  allow-downloads"` (`lib/siteFrame.ts`), toolbar reload / device width
  (Responsive, 390, 820, 1280; the width frame is shared with the HTML runner
  as `DeviceFrame`) / open in new tab (the gateway URL; system browser on
  desktop); Share is the tab's header button. A member ticket is re-minted 5
  min before expiry and used by the next load — a running page is not
  reloaded for it. A tab following the latest version remounts the viewer on
  `artifact_updated` (new ticket, new frame) and shows "Updated · vN" for 4 s;
  a version-pinned tab stays put. The workbench fetches a site's tar only to
  download or restore it.
- **Share dialog** (`views/workbench/ShareDialog.tsx`): from the tab header
  and a file card's "…" menu (Open / Share…); "Always the latest" / "This
  version (vN)", 1/7/30/90 days, create → link
  `<app base>/#/share/<token>` (gateway root in the desktop shell,
  `config.appLinkBase`) copied and shown once; list with views, creator,
  expiry/revoked/expired and revoke. Both entry points hide unless the tenant
  policy is loaded and `publicSharesDisabled` is false.
- **Public page** (`views/share/PublicSharePage.tsx`, lazy): `main.tsx` picks
  it for `#/share/<token>` (URL-safe token only, `hashRoute.shareTokenFromHash`)
  outside `AuthProvider`, so no session is read, renewed or sent and only
  `/v1/public/shares/...` is called (checked against a mock gateway). Product
  name, file name + type, "Shared by", expiry, version, size, summary,
  Download (`content?download=1`), the viewer toolbar, then the viewer; no
  viewer → download card; any 404 → "This link is no longer available".
- **Organization.** Settings tab: "Allow public share links" switch beside the
  sandbox switch. New "Public links" tab (`views/org/PublicLinksTab.tsx`):
  active links (artifact + version, room, creator, expiry, views) with a
  confirmed revoke.
- **Desktop.** `site_scheme.rs`: `bnbloc-site://localhost/v1/sites/{ticket}/{path}`
  (Windows `http://bnbloc-site.localhost/...`) → `<gateway>/v1/sites/...`
  with the query, GET/HEAD only, no session/cookie, redirects not followed,
  passes status + `Content-Type`, CSP, nosniff, referrer policy, CORS,
  caching, disposition, ETag/Last-Modified; refuses dot segments, encoded
  `.`/`/`/`\` and anything outside `/v1/sites/`. CSP `frame-src` admits it.
  The web takes only the scheme root from `convertFileSrc` (it percent-encodes
  slashes, which would break a site's relative URLs) and appends the path.
  Component downloads (`fetch_verified`) request `/v1/artifacts/{key}?direct=1`;
  reqwest drops `Authorization` on the cross-host 302 and the sha256 pin
  still decides (tested with a redirecting gateway + a separate store host).
- **Tests.** node: `lib/{shares,siteFrame,hashRoute,artifactFiles}.test.ts`,
  `views/artifact/registry.test.ts`; cargo: `site_scheme_tests.rs`,
  `sidecar_cache` redirect tests, `direct_download_url`. Screenshots (built
  bundle + mock gateway, Chromium, 1280 and 390): public markdown / site /
  download card / dead link, workbench site tab (widths, live update to v2,
  frame origin `null`, storage blocked), share dialog, Organization settings
  and Public links.
- **Follow-up (not this change).** The `bnbloc-previewer` frame URL is built
  with the whole path through `convertFileSrc`, which percent-encodes the
  slashes; unless the webview decodes them, `previewer_scheme::parse_path`
  refuses that path (it rejects `%`). Unverified in a real frame; if
  confirmed, take the same scheme-root approach as sites.
