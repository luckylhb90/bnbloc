# Multimodal chat attachments (backend-stored, .room/-delivered)

Status: implemented

Implementation notes (as landed):
- Blob store: `internal/blobstore` (BlobStore interface + disk + hand-rolled
  SigV4 S3, no new deps), content-addressed `sha256/<hex>` keys, `FromEnv()`
  picks S3 when `BNBLOC_ATTACHMENTS_S3_BUCKET` is set else disk at
  `BNBLOC_ATTACHMENTS_DIR` (dev default under a temp dir). compose mounts a
  `bnbloc-attachments` volume at `/var/lib/bnbloc/attachments`; the volume must
  be owned by the distroless nonroot uid (65532) or Put fails with a 500.
- Store: `attachments` table (both schema files + migration `2026_09_attachments`);
  `agent_jobs.attachment_ids` JSON column (migration `2026_09_agent_jobs_attachment_ids`);
  `AttachmentStore` (memory + MySQL), tenant+room scoped.
- Realtime: `POST /v1/rooms/{id}/attachments` (multipart, 25MB cap, content-type
  allowlist → 415 on reject) + `GET .../attachments/{aid}` (member-scoped stream);
  `handleChat` takes `attachmentIds`, validates room ownership, embeds metadata in
  the `chat_message` payload, and carries ids onto the agent job. Empty text is
  allowed when attachments are present.
- Gateway: `route()` sends `/attachments` paths to realtime.
- Core seam: `WorkspaceFileBytesWriter` added beside `WorkspaceFileWriter` (the
  one seam change, all protocol writeFile-capable variants implement it); wire
  `WriteWorkspaceFileRequest.ContentBase64` carries binary.
- Bridge: `materializeAttachments` downloads each attachment and writes it under
  `.room/attachments/<id>` via the bytes seam before the turn (best-effort; a
  core without the bytes seam is skipped).
- Web: composer attach button + drag/drop + paste, removable chips, 25MB client
  guard; message feed renders image thumbnails + non-image download rows.
- Verified end-to-end on the running stack: upload 200, download byte-identical,
  send-with-attachment persisted + rendered in the feed, and the guards (415
  bad type, 400 unknown id, 403 non-member download).
Scope: let a room chat message carry file/image attachments — uploaded to the
backend, shown to humans in the feed, downloadable, and made visible to the
room's agent through the existing `.room/` shared-context seam.

## Why

The composer is text-only end to end: `POST /v1/rooms/{id}/chat` takes a JSON
`{text}`, `room_messages.payload_json` holds only text, the SSE `chat_message`
normalizer reads only `text`, and the agent turn input (`SendInputRequest`) is
`{text, options}`. There is no blob storage anywhere in the repo (zero multipart
handlers, no object store, no BLOB columns). Users cannot send an image or file.

## The boundary decision this design respects

Per `docs/prd/2026-09-11-agent-collaboration-boundary-decision.md` and AGENTS.md:
bnbloc provides **addressing + authorization + visibility** and does NOT
implement agent orchestration; an agent's capabilities belong to the core
runtime, not bnbloc. So the design goal is NOT "make bnbloc feed images into a
model" — it is "put the attachment where the agent can see it, using a seam that
already exists, so that a vision-capable core uses it natively with zero bnbloc
rework."

**Delivery to the agent = the `.room/` seam, not a core-protocol change.** bnbloc
already materializes room history into the agent workspace under `.room/` via
`WorkspaceFileWriter.WriteWorkspaceFile` (bridge pull-context, e.g. `.room/live.md`);
the agent reads it with its own file tools. Attachments extend that same seam:
the bridge materializes an uploaded attachment under `.room/attachments/<...>`
and references it by workspace-relative path in the turn. A core that can read
files/images from its workspace then uses it natively. We do NOT add an
`attachments` field to the core protocol — that would be bnbloc designing the
agent's input format (out of bounds, and every core would need to change).

Rejected alternative (stop-gap, not chosen): pasting a download URL into the
prompt text. That only tells the agent a file exists without giving it a way to
read it through the core contract; it is fake multimodal.

## Storage

New object storage for binary attachments; the DB holds only metadata + a
reference. Binaries do not belong in MySQL (the nearest precedent,
`artifact_versions.content`, is MEDIUMTEXT for text deliverables, not binary).

- **Blob store**: a small storage seam `AttachmentBlobStore` with two impls:
  - local-disk (dev / single-host): a configured `BNBLOC_ATTACHMENTS_DIR`.
  - S3-compatible object store (prod): configured by env; behind the same
    interface so callers do not care.
  Content-addressed key (`sha256/<hash>`) so identical uploads dedupe and the
  key is not guessable-sequential.
- **Metadata**: new `attachments` table (id, tenant_id, room_id, uploader,
  filename, content_type, size, sha256, blob_key, created_at_ms). Follows repo
  rules: add to BOTH `internal/store/schema.sql` and `deploy/schema.sql`, plus a
  `migrations.go` migration. Dev stores are disposable — no backfill.
- **Message ↔ attachment link**: the chat message references attachments by id.
  Since `room_messages.payload_json` is a free-form map already persisted as-is,
  the attachment refs ride in the payload (`payload.attachments: [{id, filename,
  contentType, size}]`) — no room_messages schema change needed. (Attachment
  bytes are in the blob store; the message payload carries only refs.)

## API

- **Upload** — `POST /v1/rooms/{roomId}/attachments` (multipart, member-scoped
  like chat send): streams the file to the blob store with a size cap
  (configurable, e.g. 25MB, well above the 64KB chat-text cap which stays for
  text), validates content-type against an allowlist, writes an `attachments`
  row, returns `{id, filename, contentType, size, sha256}`. Rate-limited per
  subject like other cost-bearing writes.
- **Download** — `GET /v1/rooms/{roomId}/attachments/{id}` (member-scoped):
  streams the blob with the stored content-type; used by the feed image
  renderer and by anyone downloading. Membership-checked, never public.
  An `<img src>`/`<a href>` cannot carry the bearer, so the web first mints
  a ticket for that one attachment (`POST …/attachments/{id}/ticket`, 15 min,
  `services/gateway/attachment_tickets.go`) and the URL carries `?ticket=`,
  never the session (2026-10-12, code-health remediation web H10).
- **Send with attachments** — extend `POST /v1/rooms/{id}/chat` body with an
  optional `attachmentIds: string[]`; realtime validates the ids belong to the
  room, embeds their metadata into the `chat_message` payload (`attachments`),
  and — for an agent-directed message — carries the ids on the `agent_jobs` row.
- All new endpoints: add a `route()` prefix entry in the gateway (chat paths →
  Realtime), pass `svcauth.Middleware`, receive the gateway-signed
  `X-Bnbloc-User`/`X-Bnbloc-Tenant`. Upload/download bodies are streamed, not
  buffered into the 64KB reader.

## Delivery to the agent (the load-bearing seam)

When a chat with `attachmentIds` targets an agent:

1. The `agent_jobs` row carries the attachment ids (new column
   `attachment_ids` or a JSON blob — schema + migration both files).
2. The bridge, before driving the turn (where it already materializes `.room/`
   pull-context), fetches each attachment from the download endpoint and writes
   it into the session workspace under `.room/attachments/<id>-<filename>`.
   - **WriteWorkspaceFile is `content string` today.** Images are binary. This
     PRD extends that ONE seam to carry bytes (e.g. a sibling
     `WriteWorkspaceFileBytes(relPath, []byte)` or a base64/`content-type`-aware
     variant) so every core benefits — this is "extend the seam once," not a
     private copy, per AGENTS.md. A core that does not implement the byte
     variant falls back to not receiving the binary (text prompt still notes the
     attachment by name).
3. The turn prompt references the materialized path (e.g. "attached:
   .room/attachments/<id>-photo.png") so a file/vision-capable agent opens it
   with its own tools. bnbloc does not tell the model how to interpret it.

This keeps orchestration in the core: bnbloc's job ends at "the file is in the
agent's shared context, addressed by path."

## Frontend

- **Composer**: add attach affordances — a file button, drag-and-drop onto the
  composer, and paste-image-from-clipboard. Selected files show as removable
  chips/thumbnails above the textarea (mirroring the existing reference-chip
  pattern). On send: upload each via the upload endpoint, then call `sendChat`
  with the returned `attachmentIds`. Show per-file progress + error.
- **Message rendering**: `chat_message` payloads with `attachments` render image
  attachments as inline thumbnails (click → full view / download) and non-image
  files as a download row (filename + size). Images stream from the download
  endpoint (member-auth). Extend the SSE `chat_message` normalizer to carry
  `attachments`, and `room_messages` history already round-trips the payload.
- No new deps; capability-driven layout; plain-English strings.

## Security / limits

- Upload size cap + content-type allowlist (images + common docs); reject
  executables. Content-addressed keys avoid path traversal and enumeration.
- Download is membership-scoped (never public); the bridge downloads as the
  owner session it already authenticates with.
- Attachment rows are tenant + room scoped; room deletion cascades (like
  room_messages) — wipe blobs for the room.

## Acceptance criteria

- A user can attach an image (button / drag / paste), send it, and see it inline
  in the feed; another member sees it and can download it.
- A non-image file sends and appears as a download row.
- An agent-directed message with an image materializes the file under
  `.room/attachments/` in the agent workspace; a file/vision-capable core can
  open it. No core-protocol `attachments` field was added.
- Uploads are size/type limited; downloads are membership-checked; nothing is
  public.
- schema.sql + deploy/schema.sql + a migration all updated; the build is clean.

## Landing map (implementation, later)

- `internal/store`: `attachment.go` (types + memory impl), `mysql_attachment.go`,
  `attachments` table in both schema files + migration; `agent_jobs`
  attachment-ids column + migration.
- blob store: `internal/blobstore` (interface + disk + s3 impls), env wiring.
- `services/realtime`: upload/download handlers, `handleChat` `attachmentIds`
  validation + payload embedding + job carry.
- `services/gateway`: `route()` entries for the new paths.
- `services/bridge`: fetch + materialize attachments into `.room/attachments/`
  before the turn; the extended workspace-file byte writer.
- `core`: extend `WorkspaceFileWriter` with a bytes variant (the one seam
  change), implemented by cores that can accept binary workspace files.
- `web/src`: composer attach UI (button/drag/paste + chips), upload calls,
  message attachment rendering, SSE normalizer `attachments`.
- Update this PRD's status to "implemented" with commit hashes when it lands.

## Open questions to confirm before implementing

- Blob store for the internal deployment: local disk vs an available S3-compatible
  endpoint? (Determines the first concrete impl.)
- Upload size cap and content-type allowlist values.
- Whether to implement the `WorkspaceFileWriter` bytes variant for all shipped
  cores now, or land human-visible attachments first and the agent-delivery seam
  as a fast-follow (both are in-scope; sequencing only).
