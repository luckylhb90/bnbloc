# Works gallery and "make similar" (2026-10-13)

Status: accepted (product owner, 2026-10-13). This extends
`docs/prd/2026-10-13-app-gallery.md`.

Status (2026-10-13): built, every part of §2–§4 with the tests of §6.
The reference is `docs/architecture/content-components.md`, "Works".
Decided while building:
- **Type and validation.** The one type rule is `contentpkg.WorkTypeOf`
  over the deliverable as members see it (the projection's `isApp`) and,
  for a file, its name and first bytes (`store.ArtifactMIME`); an HTML
  document is a `site` (packed as its `index.html`). A file that is not a
  site is named by the manifest's `file`; the validator holds the bundle
  to its type. A file named `component.json` or `README.md` is renamed.
- **The migration** (`2026_10_content_works`) moves the rows and the
  versions' manifests (type `app`); the stored bundles keep the
  `component.json` they were saved with, and a stored version is read
  against its row's manifest (`contentpkg.ValidateStored`) rather than
  rewriting content-addressed blobs.
- **A video's cover** is the frame its producing turn saved as a cover
  file: publishing a file takes the room's image deliverable named as a
  cover file with a version from the same request (`turnCover`), so the
  room tools need no extra argument.
- **The recipe's capture is shown first**:
  `GET /v1/rooms/{roomId}/artifacts/{artifactId}/recipe`. An author's
  edit replaces the captured recipe; its references can only be a subset
  of the captured attachments. Realtime answers an attachment's metadata
  (`…/attachments/{id}/meta`) for the capture. The room tools save the
  captured recipe and report its parts (never the prompt's text) so the
  agent says what it carries.
- **Make similar** turns packs on only where the member may change the
  room's (its owner or admins, the Library's rule); in a room they may
  only post in, the packs are reported as left out. An empty recipe
  attaches the work itself: its file, or a site's files as a zip. Each
  draft counts once per room (`make-similar` use).
- **The detail** renders a work's one file from
  `GET /v1/content-components/{id}/versions/{v}/file` (also a reference
  file with `?path=`), and a site work in the sandboxed frame through the
  demo ticket; Try demo stays an app's. The Library's tab is "Works":
  the member's published works of every type, and the apps of their
  rooms.

## 1. Why

The gallery we just built only shows interactive apps, but agents in a
room make many other kinds of work: images, videos, documents, slide
decks and sites. Narrowing the gallery to apps was a limit we put on
ourselves, and we don't put limits on what members can make.

Members also learn by example. A showcase is far more useful when it
records how the work was made: the prompt, the expert, the skills and the
reference files. That recipe lets anyone press **Make similar** and start
from it.

## 2. Works, not just apps

- **One content kind, `work`** (`bnbloc.work.package/v1`). It replaces the
  `app` kind; we are pre-launch, so there is no compatibility kind. Every
  scope, visibility rule, the approval setting, favorites and covers apply
  unchanged.
  - A work is a snapshot of one deliverable version: its files and an
    optional `README.md`. Its data is never part of the snapshot.
  - The manifest's `type` field is derived from the deliverable, never typed
    by hand: `app` (a site that loads `~app/sdk.js`), `site`, `image`,
    `video`, `audio`, `document` (markdown, pdf, text), `slides`, `table`
    or `other`. `category` and `tags` work as before.
  - The schema change is additive. QA's existing `app` rows move to `work`
    in an additive migration and need no republishing.
- **Covers** keep the one cover owner, with this order:
  1. a `cover.(png|jpg|webp)` file;
  2. for an image work, the image itself (within the cover cap, otherwise
     the fallback);
  3. for a video work, an agent-made `cover.png` (the skill tells agents to
     grab a frame), otherwise the fallback;
  4. the generated fallback cover.
- **Detail view.** The work renders with the existing artifact viewers and
  previewers:
  - images inline;
  - video and audio with native player controls;
  - documents with their viewers;
  - sites and apps in the sandboxed frame.

  **Try demo** remains for `app` only.
- **Gallery.** It is renamed from "Apps" to "Works" in Plaza. A row of
  type filters (All, Apps, Images, Videos, Documents, Slides, Sites)
  appears next to the category tabs, and each card shows a small type
  badge.
- **Publishing.** Publish-to-gallery accepts every deliverable kind, not
  only sites. The route is renamed `publish-work`; no alias is kept.

## 3. The recipe

- **What it holds.** A work may carry a `recipe` in its manifest:
  - `prompt`: the text of the request that produced it.
  - `expert`: the id, name and version.
  - `skills` and `prompts`: the skill packs and prompt packs the turn
    used, by id, name and version.
  - `references`: the input files the request attached, stored inside the
    work's bundle under `references/` within the size caps.
  - `agent`: the core and provider. This is informational only; Make
    similar uses the member's own agent choice.
- **Capture.** On publish, the recipe is filled in automatically from the
  job that produced the deliverable version:
  - the request message's text and expert;
  - the room and member content uses in effect;
  - the request's attachments.

  One owner resolves "the recipe of this deliverable version", and it uses
  the existing producer lookup. When no producing job can be found (for
  example, an uploaded file), the recipe is empty.
- **Author control.** The publish dialog shows the captured recipe, and
  the author can edit the prompt, drop it entirely, or untick individual
  references. A prompt can hold internal text, so nothing is published
  without the author seeing it first. An agent that publishes through room
  tools includes the recipe and says so in its reply.
- **Display.** The detail panel shows the recipe in this order: the skills
  and expert as chips, the prompt (collapsed after a few lines, with
  "Expand"), then "Related files".

## 4. Make similar

- **Button.** The detail view has a **Make similar** button. It opens a
  small dialog to pick a room: a new room named after the work (the
  default), or an existing room where the member may chat.
- **Server.** `POST /v1/content-components/{id}/make-similar
  {version?, roomId? | newRoom:{name?}}`:
  - It checks visibility and room ownership, as `use-app` does.
  - It copies the recipe's reference files into the room as attachments
    the member owns.
  - It turns on the recipe's skill packs and prompt packs as room content
    uses, but only those the member may see.
  - It returns a **draft**: `{roomId, text, expert, attachmentIds}`.
  - It counts as a use.
- **Web.** The web opens the room with the composer prefilled through the
  existing injected-draft owner: the prompt text, the expert selected, and
  the references attached. The member edits the draft and sends it with
  their own agent.
- **Fallbacks.** If a recipe part is invisible to the member (for example
  an org-only skill in another organization), it is skipped and the dialog
  lists what was left out. An empty recipe still works: it gives an empty
  draft plus the work itself attached as the reference.
- **Apps.** Interactive apps offer both **Use this app** (an exact copy
  with an empty data space) and **Make similar** (generate anew from the
  recipe).

## 5. Out of scope for this round

- Generating media with built-in image or video models. Agents produce
  media with their own tools; the platform publishes whatever they
  produce.
- Public (cross-deployment) galleries.

## 6. Tests

- **Kind:** `work` validation for each type, and type derivation from the
  deliverable.
- **Covers:** the cover order for images and videos.
- **Migration:** QA `app` rows are readable as `work` after the migration.
- **Recipe capture:** the job's text, expert, uses and attachments are
  captured. An uploaded deliverable gets an empty recipe. An author edit is
  respected.
- **Make similar:**
  - the attachments are copied into the room, owned by the member;
  - only visible skills are turned on;
  - the draft's shape is correct;
  - an invisible work or a room the member doesn't own is refused.
- **Web:**
  - the gallery's type filters;
  - the recipe panel;
  - the make-similar draft fills the composer.
