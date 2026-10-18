# Drawing in the room: the agent draws (2026-10-17)

Status: accepted (product owner, 2026-10-17): "agent + image, together",
so the room's interaction stays one form. Replaces the composer's draw
dialog (docs/prd/2026-10-16-voice-and-image-generation.md, "the room's
draw entry"). The providers, admission, budget and audit of that PRD stay
as they are.

This PRD describes the end state. The build order (§5) is the order its
slices are built; none is replaced later.

Built (2026-10-17): §5 steps 1–3. Edits, region masks and references
(`internal/media/mask.go`, `services/gateway/media_inputs.go`,
`publishGeneratedImage`'s next-version path with the conflict fallback);
the tool's fields and the skill's editing lines; the prompts; the
composer's dialog, button and `generateImage` deleted; the workbench's
"Make a copy" (`copyArtifact`) and the viewer's exact crop (the generic
`onPublish` viewer seam). The image filter in Docs already existed. Step 4:
apps publishing and editing (`imagePlace`), styles as skill packs, and
the gallery (Docs' image filter, which already existed) are built.
Built (2026-10-18): the brush. The image viewer's "Paint an area" (a
raster image: PNG, JPEG, WebP; three brush sizes) reports a region
selection whose exact shape is a mask made when the request is sent:
the file panel uploads it as a room attachment and the selection names
it (`FileSelection.mask`, checked by realtime: a PNG attachment of the
room, on a region). The edit line tells the agent the mask's id; the
agent passes it as `region.mask`, and the gateway reads it as the
requester and sends it as the edit's mask after `media.PaintedMask`
checks it (a PNG of the input's size with something painted). The
mask is an attachment of the request, not of the message's visible
attachments row: the selection chip names it.

## 1. Why

Drawing works, but it is a side trip, and what it makes is a dead end:

- **A separate dialog with its own prompt box.** The draw button
  (`web/src/views/Composer.tsx:864-875`) opens `DrawImageDialog`. The
  member types the prompt there, not in the room, so nobody else sees
  what was asked, and nothing can be asked again.
- **The result is a throwaway attachment.** The images come back as data
  URLs and join the next message as attachment files
  (`Composer.tsx:880-887`): no versions, no Compare, no Ask AI and no
  place in Docs.
- **Two kinds of drawn image.** An agent's image is a room file (a card
  under the request, versions in the workbench,
  `services/gateway/media.go`, `publishGeneratedImage`); a member's is a
  chat attachment. Each behaves differently, in different places.
- **The image tools cannot edit.** The viewer's region prompts
  ("Remove this", "Enhance this part",
  `internal/contentbuiltin/packages/file-prompts/component.json:525-567`)
  ask the agent, but `generate_image` only draws new pictures: the agent
  has no way to change the file it was pointed at.

## 2. What it is

**Drawing is something the room's agent does.** A member asks for a
picture the way they ask for anything else, in the room; the agent turns
the ask into a good prompt and draws with the media tools; every picture
is a room file. Editing a picture is asking the agent about that file;
the result is the file's next version. There is one way to make and
change pictures, and it is the room's way.

### 2.1 Asking for a picture

- The member writes in the composer, to the agent, as always: "a launch
  poster, blue, landscape, three options".
- The agent writes the prompt (subject, composition, style, text on the
  image) from the ask and the room's context, and calls `generate_image`.
  No prompt-writing feature is needed: that is the agent's job.
- Each picture is published as its own room file in the turn's channel,
  sourced to the job (`sourceJobId`, `sourceSeq`), so the cards land under
  the request like any deliverable: in Docs, with versions, Compare and
  Ask AI.
- "Again", "another style", "make it warmer": the member says so; the
  agent draws again or edits (§2.2).

### 2.2 Editing a picture

- A member edits from the file: the image tab's Ask AI, a prompt chip, or
  the room composer naming the file. The agent gets the file and, when
  the member selected an area, the selection (the existing FileRef
  selection, `wire.FileSelection`, kind `region`, with the rectangle in
  image pixels, `web/src/views/artifact/imageView.ts:84-93`).
- The agent calls `generate_image` with `artifactId` (and `region` when a
  part is meant). The gateway reads the file's current version as the
  job's requester, asks the provider for an edit (with a mask for the
  region), and publishes the result as **that file's next version** with
  `editedFrom` (the version it was made from), the job's channel and
  source. The existing change card follows: Compare, Show, Undo.
- An edit of a picture is always its next version, whoever drew it (any
  room member may version a room file). A member who wants a separate
  file copies it first (§2.4) or tells the agent "make a copy and…".
- If someone else published a newer version meanwhile, the edit is saved
  as a new file next to it, and the tool result says so.

### 2.3 Pictures as input

- `generate_image` takes `inputs`: room files and attachments the member
  posted ("in the style of this one", "put our logo on it"). The gateway
  reads them as the requester; the first is the picture to change when
  `artifactId` is absent and the provider supports reference images, the
  rest are references the prompt names.
- The agent decides which to use; the member points at them by
  attaching, selecting or naming.

### 2.4 Copying a room file

A generic room action, for every file kind: **Copy** in the workbench
toolbar (and the agent's "make a copy") creates a new file in the same
room from the current version's bytes, named "<name> copy". Its first
version records where it came from in its summary. This is what "edit a
copy" means for pictures, and works the same for documents and decks.

### 2.5 Prompts on image files

The image prompts stay agent prompts and become exact about the tool:

- **Describe it** / **Describe this part**: unchanged (no edit).
- **Make variations**: "draw two variations of this image as new files".
- **Remove this** / **Enhance this part**: "edit this image's selected
  area with the image tool; publish it as the file's next version".
- **Crop to this**: an exact cut, done by the viewer without a model
  (the toolbar's crop publishes the cropped bytes as the next version),
  so the prompt is removed.

### 2.6 What leaves

- `DrawImageDialog.tsx` (with `dataUrlToBlob`), the composer's draw
  button, its `canDraw` / `drawing` state, and `generateImage` in
  `web/src/lib/media.ts` (no web caller remains).
- The `sel-region-crop` prompt (§2.5).

Unchanged: `POST /v1/media/image` (the tools through the room relay, and
the inline draw an app makes through `~app/ai/image`), both through
`runMediaImage`.

### 2.7 The rest of the end state

- **Brush masks.** The viewer's selection gains a brush beside the
  rectangle. The painted pixels become a mask PNG the size of the image
  (transparent where it may change); sending the request uploads it as a
  room attachment, and the selection names it (`mask`; its detail gives the
  bounding box, the edit line the mask's id). The agent passes
  it as `region.mask`; the gateway reads it as the requester, checks it is
  a PNG of the input's size, and sends it as the edit's mask instead of
  building one from the rectangle.
- **Apps draw into the room.** `app.ai.image` takes `publish: true` in a
  room app: the pictures are room files published as the participant,
  through the same publisher; the page may edit and reference the room's
  pictures (`artifactId`, `region`, `inputs`) like an agent. The app's
  room is the call's place (`imagePlace`), as a job's room is an agent's.
- **Picture history.** Docs filtered to images is the room's gallery;
  each card links to the message that asked for it.
- **Styles as content.** A style is a skill pack (how the house's
  pictures look) installed for the room or the organization; the media
  skill tells the agent to follow an installed style in every prompt. No
  new package kind: a style is guidance, which skills already are.

## 3. The tool contract

`generate_image` (the media-tools component, through the room relay):

| Field | Meaning |
|---|---|
| `prompt` | What to draw or change, concretely. |
| `aspect`, `count`, `fileName` | As today (count ≤ `wire.MaxMediaImages`). |
| `artifactId` | Edit this room file: the result is its next version. |
| `region` | `{x, y, w, h}` in the file's pixels: change only this part (a mask); with `mask` (att_…), the painted area exactly. |
| `inputs` | Room files (`art_…`) and attachments (`att_…`) to use as references. |

`wire.MediaImageRequest` gains `ArtifactID`, `Region` and `Inputs`; they
are read only under a job grant (the agent's call), as the requester.
The reply's images carry `artifactId`, `version` and `fileName`; an edit
answers the edited file's id and new version, or a new file's id with
`note` "the file changed meanwhile: saved as a new file".

The media skill (`media-skills`) gains the editing lines: an image file
the member points at is edited with `artifactId` (and `region` for a
selected area), never redrawn from scratch; references go in `inputs`.

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | **A member** asks the agent, in the room; the files' author is the job's requester. **Another member** sees the ask, the cards, and may ask the agent to change any picture (any member may version a room file). **An agent** draws and edits as its job's requester through the relay. **A share-link visitor** has no composer. **An org admin** turns AI off with `assistDisabled`: the tools answer that drawing is off. **An app** draws inline through `~app/ai/image` (and, §2.7, publishes into its room). |
| Time and lifecycle | A draw is one tool call bounded by the media timeout; a turn that ends mid-draw leaves nothing half-published (publishing follows the provider's reply). The job grant is per turn (the relay binds it at each turn's start). An edit reads the file's current version at call time and publishes against it (compare-and-swap); a newer version meanwhile becomes a new file. Every version is kept (Undo restores as the next version). |
| Scale and cost | ≤ 4 images per call (`MaxMediaImages`), inputs and outputs ≤ 12 MiB each (`MaxMediaImageBytes`), ≤ 4 reference inputs, the requester's assist rate and the cost budget, one audit row per call. The agent's turn adds a few seconds before the draw: the price of a good prompt and one interaction form. |
| Failure and recovery | Provider down or refusing: the tool returns the provider's sentence and the agent tells the member. Publish fails after drawing: the tool errors with "drawn, but saving it failed", logged with `obs.ServerError`. An input that is not a readable image, a region outside the image, a file of another room: refused before any spend, with a sentence. An edit race: a new file, said so. |
| Boundaries and privacy | Inputs are read as the requester (files and attachments they can read), never by URL. The provider key is sealed; caller auth is forwarded only for the acting member's own session (`ownAuthOf`), so an agent's call never forwards the bridge owner's. Prompts and images are never audited or logged. |
| Abuse and untrusted input | The rate, the budget, count ≤ 4, inputs ≤ 4. The provider's moderation decides what it draws. The region is checked against the image's real size. `revisedPrompt` is provider data, fenced in the tool result. |
| One owner | `runMediaImage` owns a generation (validation, inputs, provider, call, audit); `publishGeneratedImage` owns "a drawn image becomes room content" (a new file, or the next version with the conflict fallback); the room service owns ids and compare-and-swap; the media skill owns how agents use the tool; the room service owns Copy. |
| Defaults and fallbacks | No provider or AI off: the tool says drawing is unavailable; no draw entry exists to hide. A provider without masks: the edit applies to the whole image and the result says so. Square, count 1, the provider's default model unless the agent names one. |
| External contracts | `images/edits` with `mask` and several `image[]` parts is pinned by the in-repo fake (multipart fields; mask size equal to the image). The fake room service refuses what the real one does: a stale base, another room's file. |
| Load on users and agents | Members: nothing new to learn; one toolbar button fewer and one dialog fewer. Agents: three optional fields on an existing tool and a few skill lines; nothing in the session prefix. |
| Release and compatibility | Pre-launch: the dialog is deleted outright. The wire fields are additive; media-tools and media-skills versions are bumped. |
| Observability | Audit kind `assist`, name `image`, with `edit`, `region`, the input count and the published count; never content. A publish failure is `obs.ServerError` with its class. |
| AI capability | The feature is AI: the agent writes the prompt and decides draw vs edit vs reference, the image model draws. |
| Component or platform | **Platform**: the provider, admission, budget, audit, reading inputs and publishing as the requester (what a component cannot do safely), Copy. **Components and content**: the tool (media-tools), how agents use it (media-skills), the prompts (file-prompts), styles (§2.7). |

**Pre-mortem.** It shipped, and a month later it failed badly:

1. **"I asked it to fix one corner and it redrew everything."** The agent
   called without `artifactId`. The skill says an image the member points
   at is edited, never redrawn; the region prompts name the tool; the
   tool's description says the same. A test drives the prompt's turn.
2. **"Remove this changed the whole picture."** The provider ignored the
   mask. The change card shows it (Difference) and Undo reverts; the
   result says when the provider took no mask.
3. **"Two of us edited the poster and mine vanished."** The publish is
   compare-and-swap; on a conflict the edit becomes a new file and the
   reply says so. A test drives two concurrent edits.
4. **"The agent used someone's private file as a reference."** Inputs are
   read as the requester, through the room's own read rules; a file of
   another room or a link is refused.

## 5. Build order

1. **Edits and inputs.** The wire fields; reading the file and inputs as
   the requester; the mask; publishing the next version with the
   conflict fallback; the tool's fields; the skill lines; the prompts;
   tests (an edit becomes v2, a region mask's size and position, a race
   becomes a new file, an input from another room refused).
2. **The composer.** Delete the dialog, the button and `generateImage`.
3. **Copy** (§2.4) and the viewer's exact crop.
4. **§2.7**: the brush, apps publishing, the gallery filter, styles.
