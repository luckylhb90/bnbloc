# In-app Release Hub (bnbloc's own releases, config-driven)

Status: implemented

Implementation note (differs from the original data-model sketch below): rather
than a new `app_releases` MySQL table, the release history reuses the existing
`platform_settings` store — one `release.history.<version>` row per version
(JSON value), and `release.hub.{name,tagline,intro}` for the project intro. This
mirrors how installed core packages already persist a list in the same store,
so it needs zero new table / store interface / migration (reuse-before-add). The
"## Data model" section below is kept as the original design rationale; the
shipped shape is the platform_settings keys just described. Backend lives in
`services/gateway/releases.go`; frontend in `web/src/views/ReleaseHub.tsx`
(public) and `web/src/views/PlatformReleases.tsx` (super-admin editor tab).
Scope: bnbloc-only release page served by the product itself, backed by a
super-admin-maintained release history in the gateway. NOT the multi-project
static download portal (that is a separate, external artifact — see
"Relationship to the static portal" below).

## Why

A first cut shipped a `web/public/release-hub/` page that hard-coded a
`releases.json` and static-portal URLs into the product bundle. That is
wrong: the page baked an internal host + a static data file into bnbloc, so it
could not be pushed to the public mirror and could never grow richer content
without editing the bundle. It has been removed.

The page should instead be **config-driven**: bnbloc renders its own release
history from data a super admin maintains in the instance, read over a public
read-only API. Zero hard-coded hosts or data files in the bundle → the page (and
its code) is public-safe. The content model must be rich enough to grow —
version history, per-version changelog, project intro, and more surface later —
without a schema rewrite each time.

## Non-goals

- Not the multi-project portal. This page is bnbloc's own releases only.
- Not a replacement for the desktop **updater** flow (`/v1/app/update/...`).
  The updater keeps working as-is; this is a human-facing browse/download page.
  A future consolidation (updater reads the same release history) is noted as a
  follow-up, explicitly out of scope here.
- No public write path. Only super admins mutate release history.

## Relationship to the existing update-config

The current `updates.*` / `release.*` platform settings
(`services/gateway/update_config.go`) store exactly ONE current release as flat
key/values: `release.version`, `release.notes`, `release.pub_date`, and per
target `release.<target>.url` / `release.<target>.signature`. This is enough to
answer "is there a newer build for this client" but cannot express a **version
history** (a list), a **changelog** (structured, per version), or a **project
intro**. Flat key/value settings are the wrong shape for an append-only list.

Decision: introduce a dedicated release-history model rather than overloading
platform_settings. The existing update-config stays as the updater's source of
truth for now; the Release Hub reads the new history. (Follow-up: the updater's
"latest release" can later be derived from the newest history entry so there is
one source of truth — out of scope for this PRD.)

## Data model

New MySQL table `app_releases` (one row per published version of bnbloc):

| column          | type          | notes |
|-----------------|---------------|-------|
| `version`       | VARCHAR PK    | semver, e.g. `0.1.2` |
| `channel`       | VARCHAR       | `stable` \| `internal` |
| `release_date`  | VARCHAR       | RFC3339 |
| `notes`         | MEDIUMTEXT    | changelog for this version; markdown, bilingual allowed |
| `files_json`    | JSON/TEXT     | array of `{platform,arch,target,name,url,sha256,size}` |
| `created_at_ms` | BIGINT        | |
| `created_by`    | VARCHAR       | super admin subject |
| `deleted_at_ms` | BIGINT NULL   | soft-delete (hide a bad release without losing the row) |

Follows repo conventions: add the `CREATE TABLE IF NOT EXISTS` to BOTH
`internal/store/schema.sql` and `deploy/schema.sql`, AND an `ALTER`-style
creation via a new migration in `internal/store/migrations.go` (a fresh
`CREATE TABLE` in a migration is fine; the point is existing DBs get it too).
Dev stores are disposable — no data backfill.

Project-level metadata (intro/tagline/description, links, icon) is instance-wide
and low-cardinality; store it under a small set of `release.hub.*` keys in the
existing `platform_settings` (e.g. `release.hub.intro`, `release.hub.tagline`),
NOT a new table. This keeps the "project intro" editable without a schema.

### Store interface

Add to `internal/store` a small `AppReleaseStore` (memory + MySQL impls,
mirroring the existing `PlatformSettingsStore` pattern):

- `ListAppReleases(ctx) ([]AppRelease, error)` — newest-first, excludes soft-deleted
- `PutAppRelease(ctx, AppRelease) error` — upsert by version
- `SoftDeleteAppRelease(ctx, version, atMs) error`

## API

Public, read-only (session-authenticated like the current update endpoints,
since download URLs may be sensitive; NOT super-admin gated so any signed-in
user can view/download):

- `GET /v1/app/releases` → `{ project: {name,tagline,intro,...}, releases: [ {version,channel,releaseDate,notes,files:[...]} ] }`
  newest-first. This is the ONLY thing the Release Hub page reads. No host or
  path is hard-coded in the page; everything comes from here.

Super-admin, write (reuse existing `requireSuperAdminClaims`):

- `GET /v1/platform/app-releases` → full history incl. soft-deleted, for the admin editor
- `PUT /v1/platform/app-releases/{version}` → create/update one release entry
- `DELETE /v1/platform/app-releases/{version}` → soft-delete
- Project intro/tagline ride on the existing `PUT /v1/platform/update-config`
  surface or a small sibling `PUT /v1/platform/release-hub` — decide at
  implementation; prefer extending an existing super-admin settings screen over
  a brand-new page.

Validation reuses the existing helpers (`parseSemver`, `validateHTTPURL`,
RFC3339 check). File URLs must be absolute http(s). No signature is required
here (this is the human download page, not the updater manifest), but if a
`sha256` is provided it is shown for verification.

## Frontend

- Replace the deleted `web/public/release-hub/` with an in-app route/view under
  `web/src/views/` (e.g. `ReleaseHub.tsx`), reachable from the app nav.
  It reads `api.getAppReleases()` — no `fetch('./releases.json')`, no external
  host literal. Renders: project intro, a version-history list (newest first),
  per-version changelog (render `notes`), and per-file download buttons (direct
  `url`, `download` attr, human-readable size, optional sha256). Reuse the
  existing UI kit (`Card`, `Badge`, `Field`, toast) and the capability-driven
  layout primitives — NO hard-coded breakpoints, follow `web/DESIGN.md`.
- Super-admin editor: a "Releases" section (likely a tab beside the existing
  update-config under `Organization.tsx`) to add/edit/soft-delete version
  entries and edit the project intro.
- Because the page has no baked host/data, **the web bundle and this code are
  public-safe and CAN ship to the public mirror.** (The data lives in each
  instance's DB, entered by that instance's super admin.)

## Rich-content growth (why this shape)

The model is deliberately open to the richer page you want later:
- **Changelog**: `notes` is per-version free-form markdown today; can later be
  promoted to structured entries (added/fixed/changed) without breaking the API
  (additive field).
- **Project intro / marketing content**: `release.hub.*` settings; add keys as
  the page grows (screenshots URL list, highlights, etc.) — additive.
- **More surfaces** (per-version assets beyond installers, download counts,
  "latest" pinning) are additive columns/fields, not rewrites.

## Acceptance criteria

- No file under `web/` contains a hard-coded static-portal URL or a
  bundled `releases.json`. The Release Hub renders purely from `GET /v1/app/releases`.
- A super admin can add a version (version, date, channel, changelog, per-file
  download rows) and it appears in the hub, newest first, with working direct
  downloads and rendered changelog.
- Soft-deleting a version hides it from the hub but keeps the row.
- The bundle builds clean and is public-safe (the change can be mirrored to the
  public remote).

## 2026-09-21 admin editor increment

Add the super-admin counterpart inside the Platform console as a `Releases` tab.
It edits the project intro metadata and full version-history entries through the
existing platform release APIs only; it must not bake any hosts or release data
into the web bundle. Acceptance: super admins can save intro metadata, add/edit a
version with markdown notes and repeatable download files, delete a version after
confirmation, and any signed-in user sees the resulting data in the read-only hub.
- Existing desktop updater behavior is unchanged.

## Landing map (implementation, later)

- `internal/store`: `app_release.go` (types + memory impl), `mysql_app_release.go`,
  schema in `schema.sql` + `deploy/schema.sql`, migration in `migrations.go`.
- `services/gateway`: `releases.go` (public `GET /v1/app/releases`, admin
  CRUD), routes in `service.go`, reuse `requireSuperAdminClaims`.
- `web/src`: `lib/api.ts` (`getAppReleases`, admin mutators + types),
  `views/ReleaseHub.tsx`, admin editor under `Organization.tsx`, nav entry.
- Update this PRD's status to "implemented" with commit hashes when it lands.
