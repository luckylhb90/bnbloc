# Every work has a cover (2026-10-19)

Status: built (2026-10-19): §2.1–§2.2 in 9afd072e, §2.3 (the stored
version cover) after it. Extends the cover order of
`docs/prd/2026-10-13-works-gallery-and-make-similar.md` §2.

## 1. Why

QA 2026-10-19: half of the gallery's cards were the generated fallback (a
gradient and the title's first letter), an image work among them. A work's
cover comes only from a cover file in its package, and nothing makes one:

- An image work's own image is its cover only within the cover cap
  (1 MiB, PNG/WebP/JPEG). A 1536×1024 PNG from the image model is
  usually larger, so the poster fell back.
- An app or site has a cover only when the agent that made it saved a
  screenshot as `cover.png`. A single-file HTML deliverable cannot carry
  one at all: publishing refused a cover beside it.
- Slides, PDFs and documents have a first-page thumbnail in the room
  (`docs/prd/2026-10-16-file-covers.md`), which publishing never read.

## 2. What it is (the end state)

The cover order at publish, first that exists:

1. The deliverable's own cover file (a site's `cover.*`).
2. **The cover the person chose in the publish dialog**: an AI-made one
   (§2.2), or none.
3. A cover the producing turn saved beside it (`turnCover`, as today).
4. **An image work's image, fitted to the cover**: an image over the cap
   is scaled (at most 1600 pixels wide) and saved as `cover.jpg`.
5. **The room's thumbnail of the version** (the first page of a deck, a
   PDF, a document, a diagram), when someone's viewer made one.
6. The generated fallback.

### 2.1 Any work can hold a cover

- A site (a folder, or a single HTML file published as one) takes a
  cover from 2–5 as `cover.<ext>` at its root when it has none of its own;
  its own always wins. Publishing no longer refuses a cover beside a
  single-file site.

### 2.2 An AI cover, proposed in the dialog (AI first)

- The publish dialog offers **Make a cover with AI**:
  `POST /v1/rooms/{roomId}/artifacts/{artifactId}/work-cover` draws one with the
  organization's image provider from the work's title, description and
  type (the prompt is the gateway's, one owner), and answers it as a
  picture. The person keeps it, draws another or removes it; publishing
  sends it with the request (`cover`, a data URL within the cover cap).
- Nothing is drawn unless the person asks: it spends their
  organization's AI budget, the image call's budget, rate limit and audit.
- An existing work gets its cover the same way: publishing a new version
  from Library → Works opens the same dialog.
- The agent path (`save_component`) applies 1, 3, 4 and 5; an agent that
  wants a better cover saves one beside the deliverable (3).

### 2.3 A work opened without a cover gets one

A published work is immutable, so a work published without a cover kept
the generated fallback for good. A work version now has one **stored
cover** beside its bundle (as a room version has its thumbnail,
`docs/prd/2026-10-16-file-covers.md`), made the first time someone opens
it.

- **Store.** `content_version_covers` (one row per component and
  version: the blob key, the type, the rank, who stored it and when;
  migration `2026_10_content_version_covers`, additive), read and written
  through `store.ContentVersionCoverStore` (memory and MySQL, one contract
  suite). A version keeps the first cover; a strictly higher rank replaces
  it (`render` < `ai`). The bytes are a blob (blob collection counts the
  column); the rows are deleted with the component.
- **Routes** (`services/gateway/work_version_cover.go`):
  - `PUT /v1/content-components/{id}/versions/{v}/cover?source=render`:
    any member who may read the work; a PNG, WebP or JPEG body of the
    type its Content-Type says, within the cover cap
    (`tarbundle.CoverImage`: magic bytes; 413 over the cap, 415 anything
    else). Kept only when the version has no cover of its bundle and none
    stored; answers `{stored}`. No other source is accepted here.
  - `POST /v1/content-components/{id}/versions/{v}/cover/ai`: the work's
    author only (`ownContent`); drawn with §2.2's prompt from the version
    manifest's title, description and type (`drawWorkCover`: the image
    call's audit, cost budget and the assist rate limit), stored as `ai`,
    which replaces a render. 409 without an image provider, for a version
    with a cover of its own, or one that already has an AI cover (nothing
    is drawn then).
  - `GET …/cover` answers the bundle's cover first, then the stored one
    (its type, nosniff, a sandbox CSP), then 404.
  - An organization publication and a shared copy are rows of their own
    with the author's versions cloned; the same version is the same
    bundle, so they keep and read the stored cover under the author's
    component first (`coverIDs`), and their own when the author's is gone.
    One cover serves the author's card and everyone else's.
- **The detail view** (`views/gallery/useWorkCover.ts`, its plan in
  `lib/workCovers.ts`), when the cover route answered 404, once per
  version per session:
  - an image: drawn to a canvas at most 1600 pixels wide and saved as a
    JPEG within the cap (1200, then 800, as `FitCover` tries), stored as
    `render`;
  - a PDF, a deck, a document or a diagram whose viewer draws a cover
    (`cover: "render"`: the PDF viewer, a previewer declaring
    `thumbnail`): the cover its viewer sends when it renders the file,
    stored as `render`. A viewer hands its cover to its host
    (`ArtifactViewerProps.onCover`); a room's host stores it as the
    version's thumbnail (`storeRoomCover`), a work's detail as the work's;
  - a video: a frame about a second in (the middle of a shorter clip) from
    a player of its own once it has loaded data, stored as `render`;
  - anything no viewer draws (an app, a site, audio, a text file whose
    viewer only draws a text cover, other): **when the author opens their
    own work** (the listing's `mine`) and the organization's image
    generation is on, an AI cover (`POST …/cover/ai`). Nobody else's view
    draws one.
- The gallery's card asks again for a cover it was refused once the detail
  stored one (`coverStored` in `AppCover`), and the gallery's listing is
  refreshed.

### 2.4 Later

- A real screenshot of an app or site, rendered where a browser runs (the
  desktop's browser component, or a renderer component), as step 2.5.

## 3. Design review

| Lens | Answer |
|---|---|
| One owner | The cover order is `WorkCover` (read) and the publish path's cover choice (write); `FitCover` scales in one place; the AI prompt is the gateway's. |
| AI | The AI cover is proposed, a person decides; the AI-off organization sees no button. |
| Load on agents | No new tool or rule. |
| Untrusted input | A cover sent in the request is checked like any cover (magic bytes, cap, PNG/WebP/JPEG). |
| Release safety | Additive: existing works are unchanged until republished; no backfill. The stored cover is a new table and new routes; an older gateway never reads it. |
| Actors (§2.3) | A render is any reader's (a picture of what they can already open, as a room's thumbnail); an AI cover only the author's, spending their organization's budget once per version. |
| Abuse (§2.3) | A render is checked like any cover (magic bytes, cap, the type it claims) and only fills an empty place: it never replaces a cover. One per version per session from the web. |

## 4. Build order

1. `contentpkg`: a site takes a given cover; `FitCover`.
2. Gateway publish: steps 2–5.
3. `POST …/work-cover` and the dialog's AI cover.
4. The stored version cover (§2.3): store, routes, the detail view's
   render, frame and author's AI cover.
