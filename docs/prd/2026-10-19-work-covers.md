# Every work has a cover (2026-10-19)

Status: building. Extends the cover order of
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
it:

- `PUT /v1/content-components/{id}/versions/{v}/cover?source=render|ai`
  stores it (PNG, WebP or JPEG within the cover cap, checked by magic
  bytes). `render` is any reader's and is kept only when the version has
  none; `ai` is the author's and replaces a rendered one. The cover route
  answers the bundle's cover first, then the stored one, then 404.
- The detail view, when the cover route answered 404:
  - an image, a PDF, a deck, a document or a diagram: its viewer's
    thumbnail (the previewers' `provideThumbnail`, as in a room), stored
    as `render`; no AI, nothing spent;
  - a video: a frame of its player, stored as `render`;
  - anything no viewer draws (an app, a site, audio, other): **when the
    author opens their own work**, an AI cover is drawn from its title,
    description and type (§2.2's prompt) and stored as `ai`. Only the
    author's opening spends their organization's budget, once per
    version; nobody else's view draws one.
- The gallery's card asks again for a cover it was refused once the
  detail stored one.

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
| Release safety | Additive: existing works are unchanged until republished; no backfill. |

## 4. Build order

1. `contentpkg`: a site takes a given cover; `FitCover`.
2. Gateway publish: steps 2–5.
3. `POST …/work-cover` and the dialog's AI cover.
4. The stored version cover (§2.3): store, routes, the detail view's
   render, frame and author's AI cover.
