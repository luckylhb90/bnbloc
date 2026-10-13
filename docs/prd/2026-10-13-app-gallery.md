# App gallery: publish an interactive app, browse, use a copy (2026-10-13)

Status: proposed. The product owner approved the direction and the order on
2026-10-13. Build after the share-link sign-in change
(`docs/prd/2026-10-13-share-link-sign-in.md`) and the "Apps" views (§5) land.

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
   from the card, and agent publishing.
3. The `cover.png` convention in the skill pack, with the drift test kept.
4. The Plaza gallery: grid, categories, search, favorites, detail page and
   demo mode.
5. "Use this app" copy flow.

Each step lands with its tests:
- validator cases;
- visibility;
- favorites contract (both stores);
- a demo mode that never calls the data API;
- a copy whose data space starts empty and is separate from the original;
- cover type and size checks.

## 11. Open questions

- **Q1. Who may publish an app to the org gallery?** Proposal: any member,
  through the existing org approval, exactly as experts.
- **Q2. Should "Try demo" exist, or only "Use this app"?** Proposal: keep
  demo. It is cheap (an in-memory SDK) and it is what makes browsing feel live.
