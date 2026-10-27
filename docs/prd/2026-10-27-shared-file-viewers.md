# A shared file renders in its viewer, for people who have nothing installed (2026-10-27)

Status: built locally (2026-10-27), not yet deployed or released. Builds on
`docs/prd/2026-10-09-artifact-shares-and-site-previews.md` (the share routes),
`docs/prd/2026-10-13-share-link-sign-in.md` (who may open a link, the share
ticket) and `docs/architecture/previewer-components.md` (the frame route).

## Why

A member shared a `.excalidraw` file with a link (`/s/<token>`) and the page
showed its JSON as code. The same happens to every file whose viewer is an
installed previewer: Office, draw.io, Mermaid, Excalidraw. The share page cannot
use them, for two reasons found in the code on 2026-10-27:

1. The share page reads the viewer registry, but the registry is filled only by
   the signed-in app (`App.tsx`, `startPreviewerSync`), from the member catalog
   (`GET /v1/components?kind=previewer`, a session route). The share page
   renders before sign-in and never reaches it.
2. The frame route's ticket is minted by that catalog for the signed-in caller.

The built-in viewers (Markdown, image, app, site, PDF, table, JSON, media, code)
do not claim those types, so the page falls back to code or to a download.

The product owner's requirement: **a recipient who has never used the product,
and has nothing installed, must see the file.** Components are opt-in per member
(`optin`), so nothing of the recipient's can be assumed.

## Decisions

1. **The sharer's viewers decide.** A share renders with the previewers
   available to *the link's creator* in the link's organization: what the sharer
   sees is what the recipient sees. The recipient installs nothing and needs no
   account beyond what the link's audience already asks (`anyone`: nothing;
   `signed_in`: an account; `org`: a member of the organization, the default).
2. **A cover shows at once, the viewer takes over.** The recipient first sees
   the version's stored cover (the picture a previewer already makes for the
   file card), then the interactive viewer replaces it when it has drawn. A
   five-megabyte bundle never leaves a stranger on a blank page.
3. **The recipient views; nothing else.** A viewer on a share runs view-only:
   no save, no draft, no live state, no selection, no member action.

## What exists (checked against the code, 2026-10-27)

- `openShare` (`services/gateway/share_access.go`) resolves a credential (a
  link token, or a share ticket) and `shareAccess` decides who may open it.
  Every share route goes through `publicShare`, which also rate-limits per IP
  (`publicAllow`) and spends a guess budget on a miss.
- The resolution carries the link's `CreatedBy` and `TenantID`
  (`wire.ShareResolution`), so the creator's identity is available.
- The previewer frame route is `open(...)` ("a previewer ticket in the path"):
  no session. Its ticket is a signed claim (subject, tenant, `name@version`);
  on every request `visiblePreviewer` re-reads the **ticket subject's**
  installed state and the rollout and tenant gate. The ticket reads only that
  bundle's files and grants nothing else (previewer-components.md).
- Shared web apps already run member-authored code for anonymous visitors, from
  the sites origin, with the link's credential in the path. A signed, reviewed
  previewer in an opaque-origin sandbox with `connect-src 'none'` is a smaller
  risk than that.
- Covers: a version stores one thumbnail, PNG or WebP, at most 512 KiB and 1024
  px a side, with a rank of its source (`ArtifactThumbnailRank`); previewers
  produce it in a sandboxed frame (`preview.thumbnail`).

## Design

### 1. Share-scoped previewers (gateway)

`GET /v1/public/shares/{token}/previewers`, an open route like its siblings:

- `publicShare(w, r, false)` first: rate limit, `openShare`, `shareAccess`.
  Does not count a view.
- **Eligibility of the creator.** The creator must still be a member of the
  link's organization and the organization not closed (`memberRole`,
  `tenantClosed`: the same reads `shareAccess` makes for an `org` link). If not,
  the answer is an empty list.
- **Which previewers.** `loadMemberComponents(ctx, claims)` with
  `claims{Subject: creator, Tenant: link tenant}`; a previewer qualifies when its
  visibility for that viewer is `Installed` (installed, or `auto`, or managed)
  and its `match` claims the shared file (extension, MIME type, `type/*`). The
  filter keeps the answer to what this file needs and does not publish the
  organization's whole set.
- **What is returned.** `{"components": [...]}`, each entry the minimal member
  catalog shape the web parser already reads (`kind`, `installed`, `name`,
  `version`, `description`, and `previewer` with `entry`, `match`, `rank`,
  `capabilities`, `toolbar`, `draft`, `bundleSha256`, `frameUrl`,
  `frameExpiresAtMs`; `bundleUrl` empty). No permissions, settings, publisher,
  artifacts or counts.
- **The ticket.** The existing `KindPreviewerFrame` ticket with the creator as
  subject and the link's tenant, minted by the same function the member catalog
  uses (`previewerCatalog`), lifetime one hour. The frame route is unchanged:
  every request re-checks the **creator's** installed state, so a sharer who
  uninstalls a previewer takes it away from the link at once. A sharer who
  leaves the organization ends new lists at once (the eligibility above); the
  tickets already minted last until they expire, an hour, because the frame
  route checks the install and not the membership.
- `Cache-Control: no-store`.

### 2. Cover (gateway, room service)

- `wire.ShareVersion` gains the version's thumbnail key and media type
  (internal to the resolve call; never in a public payload). The room service
  fills them from the version.
- `publicShareView` gains `cover: true` when the resolved version has one.
- `GET /v1/public/shares/{token}/thumbnail`: `publicShare`, then the stored
  image, `Content-Type` image/png or image/webp, `nosniff`,
  `Content-Security-Policy: sandbox`, `Cache-Control: no-store`; 404 when the
  version has none. A link that follows the latest version serves the latest
  version's cover.

### 3. View-only (frame protocol, SDK, host)

- `preview.init` gains an optional `viewOnly: true` (`PreviewInit.viewOnly` in
  the SDK). A previewer that predates it ignores it, so the host does not trust
  the frame to comply:
- the host **refuses**, for a view-only frame, `preview.save`, `preview.draft`,
  `preview.state.send` and `preview.selection`, whatever the frame declares;
- `ArtifactViewerProps.viewOnly` carries it from the share page to the host. The
  same flag serves an older version in the workbench later.

### 4. The share page (web)

- It reads the share's previewers (`publicShareApi.previewers`) and registers
  them in the viewer registry for this page. The registry's source becomes
  pluggable: the signed-in app reads the member catalog, the share page reads the
  share's; ticket renewal uses the same source.
- **No flash of code.** The viewer is chosen only after that lookup ends (or
  fails), so a file with a previewer is never shown as code first.
- **Poster, then viewer.** If the share has a cover and the viewer is a
  previewer, the page shows the cover (an `<img>`, contained) while the frame
  loads out of sight; it swaps when the previewer reports its first render
  (`preview.thumbnail`), on `preview.error`, or after a limit (20 s), so a frame
  that never reports does not hide behind a picture.
- A failed lookup, a creator who can no longer offer it, or no match leaves the
  page as it is today (a built-in viewer, or the download).

### 5. The Excalidraw previewer (a component)

On `viewOnly` it uses Excalidraw's view mode and hides the footer (save, draft).
It needs no other change; this is the first previewer to honour the flag.

## Placement

| Part | Where | Why | What an organization would swap |
|---|---|---|---|
| Which viewers a share may use, who may open it, the ticket, the cover route, the creator check | **Platform** | identity and the attested caller, signing, the member-authenticated proxy; events and routes only the platform can authorise | nothing: it is the existing access rule |
| `viewOnly`, the refusal of save, draft, state and selection | **Platform** (contract) | the contract between host and frame, and confinement of what a frame may ask | nothing |
| Drawing the file, honouring view mode, making the cover | **Component** (each previewer) | its own format and runtime | the previewer |
| Poster-then-viewer on the page | **Platform** (web host) | the page owns when to show what | nothing |

Tests. *Name*: the platform names no previewer, file type or product. *Fork*: an
organization with its own viewer installs it; no platform change. *Replace*: a
second previewer for the same type installs beside the first. *Reason*: identity,
confinement, the share's own access rule.

## Design review

1. **Actors.** The creator (their installs decide), the recipient (anyone,
   signed in, or an org member, by the link's audience), the organization's
   admin (the link audience switches already exist). Agents create shares too;
   if the creator has no installs the link renders as today.
2. **Time and lifecycle.** A ticket lasts one hour and the frame route re-checks
   the creator's install on every request, so an uninstall takes effect at once.
   A creator who leaves the organization ends new lists at once and old tickets
   within the hour. A link that follows the latest version serves the latest
   version's cover and bytes.
3. **Scale and cost.** One more open request per page view (rate-limited per IP
   like its siblings) and one cover request. The bundle is downloaded per visit:
   the frame base carries a per-visit ticket, so a browser cannot reuse a bundle
   across links. **Not measured**; the cover is what makes the first view cheap.
   A content-addressed, credential-free bundle address would remove it and is an
   open point, not part of this change.
4. **Failure and recovery.** Every failure degrades to today's page (a built-in
   viewer, or the download), never to an error. The poster has a timeout.
5. **Boundaries and privacy.** The answer names only previewers that claim this
   file, with no permissions or settings. The frame is the same opaque-origin
   sandbox with no network. A recipient reads only the shared version's bytes
   through the share's own content route.
6. **Abuse and untrusted input.** A public route can be hit: `publicAllow` per IP
   as for `/content`. The shared file is untrusted input to the previewer, as it
   already is in a room, inside a sandbox with no network. The cover is an image
   served as `<img>` only, with `nosniff` and `sandbox`.
7. **One rule, one owner.** `shareAccess` stays the only rule of who may open a
   link; `previewerCatalog` the only minter of a frame ticket; the web's
   registry is filled through one source interface. No second copy of either.
8. **Defaults and fallbacks.** No setting is added. The default is on, bounded by
   the link's own audience and the creator's installs.
9. **External contracts.** Two routes, one field of the public share view
   (`cover`), one optional field of `preview.init`. An older host or previewer
   ignores what it does not know.
10. **Load on users and agents.** None for members or agents; recipients see a
    picture, then the viewer.
11. **Release and compatibility.** Gateway and room service first (the routes),
    then the web, then the previewer's view-only handling. A new page against an
    old gateway gets a 404 from the route and renders as today.
12. **Observability.** A counter of share previewer lists served and empty, and
    of covers served, by outcome. No file content or token in any log.
13. **AI capability.** None here, deliberately: this is a record of who may see
    a file, not a decision. Draft and Ask AI are off for a view-only frame.
14. **Content, component or platform.** See the table.

**Pre-mortem.** It shipped and a month later it failed badly:

1. *"The external person still sees code."* The viewer is chosen only after the
   lookup, the lookup degrades quietly, and a test opens a share with no
   previewer, with one, and with a failing lookup.
2. *"A stranger waits on a blank page."* The poster; a share with no cover shows
   the existing loading state, which is the same as today.
3. *"A sharer left and the links broke."* Documented and tested: they render as
   today. The product owner has accepted this rule (the sharer's viewers decide).
4. *"The organization's component list leaked."* Filtered to what claims the
   file, minimal fields, nothing else.
5. *"Someone used the route to pull bundles."* A ticket is minted only behind
   `publicShare` (rate-limited), lasts an hour, and reads one bundle. The frame
   route's own limits are not changed here; **not verified** whether it needs one.

## Verification (to run)

- Gateway: for a link of each audience, the previewers route returns the
  minimal entries for a matching file, nothing for a non-matching one; the
  ticket loads the bundle's entry through the unchanged frame route with no
  session; a creator who left the organization, or has not installed it, gets an
  empty list; `org` and `signed_in` links refuse an anonymous caller as their
  siblings do; the cover route serves, 404s without one, follows the latest.
- Web: the registry source, no flash of code, the poster's swap, timeout and
  error paths, view-only refusals in the host.
- Component: `excalidraw/build.test.mjs` and the browser check in view-only.
- On QA after deploy, with a throwaway room: a share of an `.excalidraw` file
  opened with no session (an `anyone` link, if the organization's switch is on)
  and with a share ticket (an `org` link): the previewers route, the cover, the
  frame's entry, the content.

Not run before release: the desktop app's behaviour for a share link, a real
external browser on a mobile network, and the bundle's cost per visit.

## As built (2026-10-27)

Where the code differs from the text above, or found something the text had
wrong:

- **Leaving the organization.** The frame route re-checks the ticket subject's
  *install*, not their membership. A creator who uninstalls cuts a link at once;
  one who leaves ends new lists at once and old tickets within the hour. The
  design review above says so now.
- **The entry type.** `sharePreviewerEntry`, not `wire.MemberComponent`: that
  type's non-omitempty fields (`rollout`, `managed`) would add keys to the
  answer. `bundleUrl` is present and empty.
- **The matcher** is `filematch.Claims` in `internal/filematch`, the one owner
  of the match vocabulary, not a gateway helper.
- **Metrics.** The gateway had no domain registry; `Service.Metrics` is new and
  served where the other services serve theirs (never on the public listener).
  Outcomes of the previewer list: `listed`, `empty`, `creator-gone`, `closed`
  (the organization is suspended), `unverified` (no tenant directory: fails
  closed, as `shareAccess` does).
- **The ticket's role** is the lowest (`viewer`); it reads one bundle and nothing
  else.
- **View mode is the host's, not the file's.** Excalidraw's `restoreAppState`
  returns a whole state with view mode off, which undid the mode in WebKit; the
  component sets it again after loading a scene.
- **The desktop shell.** A desktop frame loads from the shell's own
  `bnbloc-previewer` scheme, out of the member's own catalog, so a previewer only
  the sharer has would not load there. The share page does not look up share
  previewers inside the shell and shows what it showed before. Not changed here.
- **No extra wait for built-in files.** The page does not choose a viewer before
  the previewer lookup answers, but it starts the file's bytes at once when a
  built-in viewer already claims it (an image, a PDF), so those do not pay a
  second round trip. A file nobody claims yet waits for the lookup before its
  bytes (it may be a large download-only file).
- **The poster** is shown once a previewer is the chosen viewer, so during the
  lookup itself the page still says "Loading preview…" (one round trip).

## Not in this change

- A static rendition (SVG or PDF) for cards, link previews and search.
- A credential-free, content-addressed bundle address (cache across links).
- Server-side rendering.
- A per-organization switch for previewers on `anyone` links (the link audience
  and the organization's `anyone` switch already bound it).
