# App gallery: publish an interactive app, browse, use a copy (2026-10-13)

Status: accepted (2026-10-13). The product owner approved the direction, the
order and §11. Build after the share-link sign-in change
(`docs/prd/2026-10-13-share-link-sign-in.md`) and the "Apps" views (§5) land.

Status of the server side (2026-10-13): steps 2, 3 and 5 are built. The
content kind `app` and its validator, publishing from a site (route and
room tools), cover serving, favorites, "Use this app" and the
`publishApproval` organization setting (with its field in Organization →
Settings) are in; the cover convention is in the `interactive-apps` skill
pack (1.1.0). The reference is `docs/architecture/content-components.md`,
"Apps". Open: the fallback-cover owner, the "Publish to gallery" card
action and the gallery itself (steps 1, 2's web part and 4).

## 1. Why

Members build interactive apps in rooms (`docs/prd/2026-10-12-interactive-
apps.md`). A good app is useful beyond its room. Others should be able to find
it, see what it looks like, and get their own copy to use or adapt. A gallery
of cards with real screenshots, categories, search and favorites makes apps
discoverable and makes the product feel alive.

## 2. Principles

- **Reuse the content-component system.** Scopes (personal, shared, org,
  platform, built-in), org approval, platform signing, visibility
  (`contentuse.Visible`) and the Library already exist. An app is one more
  content kind, not a new subsystem.
- **A published app is a template, not a live instance.** The gallery shows
  the app's look and lets a member try a **demo with no data access**, or
  **use a copy**. A copy is a new site in the member's own room with its own,
  empty data space. Nobody ever sees the original's data through the gallery.
- **Minimal platform.** The platform stores and lists templates and copies
  them. What an app does is the app's own business.

## 3. Content kind `app`

- **Bundle.** The site folder as published: `index.html` or `dist/`, the same
  limits as site deliverables, through `tarbundle`. It may also contain:
  - `cover.png` (or `.jpg` / `.webp`), the card cover, with a size cap and
    16:10 recommended;
  - `README.md`, the description shown on the detail page.
- **Manifest** (`component.json`): the common component fields (`name`,
  `displayName`, `description`, `version`, `tags`), plus optional
  `category`. No app-specific runtime fields.
- **Validation.** A `KindSpec` registered in `internal/contentpkg`:
  - the entry point exists;
  - the cover is a real image within its cap;
  - no bnbloc-owned or host-executable paths (`core.ProtectedWorkspacePath`).
- **Categories.** One wire constant list, rendered as the gallery's tabs: Office,
  Data, Productivity, Content, Learning, Engineering, Finance, Life, Other.
  Any other value is refused.

## 4. Covers

1. **Agent-made cover (primary).** The built-in `interactive-apps` skill pack
   tells the agent to screenshot the app's first screen with a headless
   browser in its own workspace, after it builds, and save the image as
   `cover.png` in the site folder. The bridge publishes it with the site. No
   platform service is needed, and the cover shows the real app.
2. **Fallback cover.** With no cover, the web renders a generated cover from
   the app's name and a deterministic gradient and icon, seeded by the
   component id. One owner (`lib/appCover.ts`). Cards are never blank.
3. **Later, optional.** A server-side screenshot component (headless Chromium)
   for uniform covers, as an installable package. Not in this round.

## 5. "Apps" views (before the gallery)

Status: **landed** (2026-10-13), except "Publish to gallery", which comes
with step 2. Decided while building:
- **One rule, one owner: `store.Artifact.IsApp`.** A site whose data space
  exists (its key is stored and not being destroyed) or whose current
  version loads `~app/sdk.js`. Every read of an artifact fills both facts;
  `sqlArtifactIsApp` is the SQL twin beside the Go rule; the projection's
  `isApp` is the one way out (deliverables, `?app=1`, `GET /v1/my/apps`,
  the agents' `.room/apps.md` and `app_data list`, the web). An app nobody
  used yet is now an app (it loads the SDK) and is listed with 0 documents.
- **The SDK is detected once per version, while its bytes are stored.**
  The room service tees a site bundle through `appdata.SDKScan` on its way
  into the blob store (publish and import alike) and stores the answer on
  the version (`artifact_versions.loads_app_sdk`, migration
  `2026_10_artifact_app_sdk`). A site bundle is an uncompressed ustar, so a
  substring scan of the stream sees every file's bytes; nothing re-reads a
  bundle to list. Versions stored before the migration read as not loading
  it (pre-launch; their data space still makes them apps).
- **`GET /v1/my/apps`** (the room service; `wire.MyAppList`): the apps of
  the rooms of the effective tenant where the caller may chat (an owner),
  newest-updated first, keyset-paged (24, at most 60). Each: app id, name,
  room id and name, latest version and update time, producer (the source
  job's agent, else the publishing person: `roomlog.ProducerOf`, now the
  one producer rule for agents and the listing), the document count (read
  only for an app with a data space) and the working links (access,
  expiry).
- **Covers.** `GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{v}/cover`
  (a member's room read): the first of `cover.png`, `cover.webp`,
  `cover.jpg`, `cover.jpeg` at the site root, only when its bytes sniff as
  that type, at most `wire.MaxAppCoverBytes` (1 MiB), with nosniff and a
  sandbox CSP; an SVG is never a cover. 404 without one, 413 over the cap,
  415 for anything else: the card then draws the generated cover
  (`lib/appCover.ts`). The room's deliverable cards of apps show the cover
  too.
- **The deliverables panel** has an "Apps" type (server-side `?app=1`, and
  the same `isApp` on the page); "Web & apps" became "Web pages & sites",
  and a site's type label is "Website", an app's "Interactive app".

- **The room's deliverables panel** gets an "Apps" filter: sites whose data
  space exists, or that load `~app/sdk.js`. Use the same rule as
  `roomlog.ReadApps`, extended to sites with the SDK reference. One owner.
- **Library → "Apps" tab ("My apps").** Apps in rooms I own or administer,
  across rooms. Each card shows:
  - the cover;
  - the name, room and producer;
  - link status (access mode `org` / `anyone`, expiry);
  - document count and last update.

  Actions: open, share or manage link, export, go to room, and **Publish to
  gallery**.

## 6. Publishing

- **From the app card, "Publish to gallery":**
  - snapshot the current site version's bundle (plus cover and README) as an
    `app` component;
  - personal scope first;
  - then **publish** to org (approval) or platform (signed) through the
    existing flow.

  Data is never part of a template.
- **Updating.** "Publish new version" snapshots the latest site version as the
  next component version. Rooms that used a copy keep their copy.
- **Agents.** The room tool `save_component` / `publish_component` accepts kind
  `app` with a site artifact id, so an agent can publish an app on request.

## 7. Gallery (Plaza → "Apps")

- **Placement.** Plaza gains an "Apps" section next to rooms.
- **Grid.** A grid of cards: cover, name, author (person or "Platform"), and a
  favorite star.
- **Filters.** Category tabs, search (name, description, tags), and "My
  favorites".
- **Detail page:**
  - cover;
  - README rendered as markdown;
  - author, version, updated time;
  - a **Try demo** button: the bundle runs in the sandboxed site frame with
    the SDK in **demo mode**, an in-memory data space that lives only in this
    page and is never persisted or shared;
  - **Use this app**.
- **Favorites.** A small per-member table (`content_favorites`), with a store
  contract suite and per-tenant scope.
- **Visibility.** `contentuse.Visible`, as for every content kind (org
  content in its tenant, platform content where enabled).

## 8. "Use this app"

- The member picks a room: an existing room they own, or a new room named
  after the app.
- The bundle is copied as a new `site` artifact version in that room,
  attributed "from <app>@<version>", with a fresh, empty data space keyed by
  the new artifact id.
- The member can then ask an agent in the room to adapt it. With the
  `interactive-apps` skill, it edits the copied files like any site.
- A copy counts as a use of the component (`content_component_uses`,
  per tenant) for "used by N" on the card.

## 9. Security

- Templates hold no data, and demo mode never reaches a real data space.
- A cover is an image: served with `nosniff`, a type check and size caps;
  never an SVG with script.
- Template bundles run only in the sandboxed site frame (sites origin when
  configured), like any site.
- Org approval and platform signing apply as for every content kind.

## 10. Delivery steps

1. The "Apps" filter and Library "My apps" (§5).
2. Content kind `app`: validator, store, the fallback-cover owner, publishing
   from the card, and agent publishing; the org `publishApproval` setting (§11).
   Server side done: `contentpkg` kind `app`, `POST /v1/rooms/{roomId}/
   artifacts/{artifactId}/publish-app`, `save_component` with `artifactId`,
   the cover route, favorites (`content_favorites`, migration
   `2026_10_app_gallery`), `contentshare.NeedsApproval` behind every publish
   path.
3. The `cover.png` convention in the skill pack, with the drift test kept.
   Done.
4. The Plaza gallery: grid, categories, search, favorites, detail page and
   demo mode.
5. "Use this app" copy flow. Server side done:
   `POST /v1/content-components/{id}/use-app`.

Each step lands with its tests:
- validator cases;
- visibility;
- favorites contract (both stores);
- a demo mode that never calls the data API;
- a copy whose data space starts empty and is separate from the original;
- cover type and size checks.

## 11. Decisions (product owner, 2026-10-13)

- **Who may publish to the org gallery.** Any member may publish. Approval
  follows the organization's setting below.
- **Try demo.** Kept: the in-memory demo mode described in §7.
- **Org publication approval is an organization setting.** It covers every
  content kind (experts, skill packs, prompt packs and apps) under one rule.
  - The setting is `publishApproval: "required" | "none"`, stored with the
    tenant settings and edited by org admins in Organization → Settings.
    It defaults to `"required"`.
  - With `"none"`, a member's publish to the org goes live at once. It is
    still audited, and admins can still unpublish, restore or remove it.
  - Platform scope is unaffected and stays a signed install by a super
    admin.
  - One owner decides whether a publish needs a request:
    `contentshare.NeedsApproval(tenant settings)`. Every publish path calls
    it.
  - Its tests:
    - both settings, for every kind;
    - the default is `"required"`;
    - unpublishing still works.
