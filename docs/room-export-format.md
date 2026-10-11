# Room handoff package format (`bnbloc.room-export/v1`)

A handoff package hands a room's work to someone else, or carries it to
another deployment: the conversation, the room's artifacts and attachments,
and the agent session metadata the platform holds — as one zip. It is produced
by `GET /v1/rooms/{roomId}/export` and consumed by `POST /v1/rooms/import`.
The Go DTOs are `internal/wire/handoff.go`; that file and this document are
the contract.

## Routes

| Route | Who | What |
| --- | --- | --- |
| `GET /v1/rooms/{roomId}/export` | room owner or admin | Streams the package (`application/zip`, `Content-Disposition: attachment; filename="<room>-handoff-<yyyymmdd>.zip"`). |
| `POST /v1/rooms/import` | any member with the org `create_room` permission (rate-limited like room creation) | Multipart upload, field `file`. Creates a **new** room owned by the caller in the caller's tenant. Answers `RoomImportResult`. |
| `GET /v1/rooms/{roomId}/handoff` | room member | The room's import provenance (`RoomHandoffInfo`); `404` for a room that was not imported. |

Export is restricted to owner/admin: those are the roles the room model already
reserves for acting on the room as a whole (invites, member management,
channel deletion), and a package is a bulk copy of every member's messages,
files and artifacts that leaves the room's access control.

## Layout

```
manifest.json
messages.jsonl
transcript.md
attachments/<attachmentId>/attachment.json
attachments/<attachmentId>/<filename>
artifacts/<artifactId>/artifact.json
artifacts/<artifactId>/v<N>.<md|html|txt>     (inline text versions)
artifacts/<artifactId>/v<N>.<file ext|bin>     (blob / file versions)
```

Entry order is not significant (the exporter writes `manifest.json` last,
because it carries the counts). Path segments derived from ids and filenames
are restricted to `[A-Za-z0-9._-]` (anything else becomes `_`).

### `manifest.json`

```json
{
  "format": "bnbloc.room-export/v1",
  "exportedAtMs": 1791400000000,
  "exportedBy": "alice",
  "room": { "id": "launch", "name": "Launch plan", "topic": "Q4 launch", "createdAtMs": 1791000000000 },
  "channels": [
    { "id": "ch_…", "name": "general", "isDefault": true },
    { "id": "pc_…", "name": "People chat", "isDefault": false, "kind": "people" }
  ],
  "members": [{ "userId": "alice", "role": "owner" }, { "userId": "bob", "role": "member" }],
  "agents": [{ "owner": "jack", "provider": "codex", "core": "acme" }],
  "agentSessions": [{
    "agentSessionId": "…", "owner": "jack", "provider": "codex", "channelId": "ch_…",
    "messageCount": 4, "firstAtMs": 1791300000000, "lastAtMs": 1791300900000
  }],
  "messageCount": 42,
  "artifactCount": 2,
  "attachmentCount": 3
}
```

- `format` must equal `bnbloc.room-export/v1`; an import refuses any other
  value (`400 unsupported package format`).
- `channels[].kind` is `"people"` for the room's people chat (the
  human-only side conversation agents never see) and absent for an ordinary
  channel. Its messages are in `messages.jsonl` like any other channel's.
- `members` is **informational only**: an import never grants anyone access
  from it. It is stored as invite suggestions for the new room's owner/admins.
- `agents` lists every agent handle (`owner/provider`) the feed shows, from
  agent replies and agent-addressed chat; `core` is filled when an
  agent-addressed chat recorded it.
- `agentSessions` is the platform's view of each agent session in the feed (id,
  attribution, channel, message count, first/last time). Core-internal state
  (the agent runtime's own session/memory) is not included: it lives in the
  agent runtime, not in bnbloc's store.

### `messages.jsonl`

One persisted feed row per line (`chat_message` and `agent_messages` only),
ascending by `sequence`, soft-deleted messages excluded. Each line is the room
history row shape plus the row's channel:

```json
{"sequence":12,"channelId":"ch_…","kind":"chat_message","payload":{"userId":"bob","text":"…","recipient":{"ownerId":"jack","provider":"codex"},"attachments":[{"id":"att_…","filename":"mock.png","contentType":"image/png","size":5120}]},"atUnixMs":1791300000000}
{"sequence":13,"channelId":"ch_…","kind":"agent_messages","payload":{"workspaceId":"launch","agentSessionId":"…","channelId":"ch_…","messages":[{"seq":1,"createdAtUnixMs":1791300900000,"payload":{"role":"assistant","text":"…","ownerUserId":"jack","provider":"codex"}}]},"atUnixMs":1791300900000}
```

`editedAtMs` is present on a row that was edited.

### `transcript.md`

A human-readable rendering of the same rows: chat as `**user** · time`, agent
replies attributed to their `owner/provider` handle and marked `(agent)`,
attachment names listed, the channel shown when the room has several.
Informational; an import does not read it.

### `attachments/`

Every attachment an exported chat message references, as its bytes plus
`attachment.json`:

```json
{ "id": "att_…", "filename": "mock.png", "contentType": "image/png", "size": 5120,
  "sha256": "…", "file": "attachments/att_…/mock.png" }
```

An attachment whose blob is no longer stored is left out; the message keeps
its reference and an import drops it.

### `artifacts/`

Every room artifact with its full version history: one file per version plus
`artifact.json`:

```json
{ "artifactId": "art_…", "channelId": "", "kind": "markdown", "title": "Spec",
  "mimeType": "text/markdown", "fileName": "spec.md",
  "currentVersion": 2, "createdBy": "alice", "createdAtMs": 0, "updatedAtMs": 0,
  "versions": [{ "version": 1, "contentHash": "…", "storage": "inline",
                 "mimeType": "text/markdown", "sizeBytes": 4, "summary": "",
                 "authorUserId": "alice", "createdAtMs": 0, "file": "artifacts/art_…/v1.md" }] }
```

A **blob** version (`"storage": "blob"`, a file deliverable such as an image
or PDF) is exported as its raw bytes, named by the artifact's file extension
(`v1.png`; `.bin` when there is none); its `contentHash` is the sha256 of
those bytes. `sourceJobId` / `sourceSeq` record which agent job and room
message produced a version in the source room; they are informational and
not imported. Version thumbnails are derived data and are not exported.
A `site` artifact's versions are blobs of type
`application/vnd.bnbloc.site+tar` (the site snapshot tar); import refuses a
site version of any other storage or type, and that type on any other kind.
Public share links of artifacts are not exported (a link belongs to the
source room's organization and dies with it).

## Import semantics

1. **Validate everything first** — nothing is created for a refused package
   (`400` with the reason): zip structure (no absolute/`..`/duplicate entry
   names), manifest format and fields, channels (unique ids, at most one
   default, `kind` absent or `people`, at most one people chat and never the
   default), no agent traffic in the people chat (an `agent_messages` row or
   a chat with an agent `recipient` there is refused), every attachment's sha256 and every artifact version's content
   hash, and every `messages.jsonl` line (known kind, JSON-object payload, a
   channel listed in the manifest, strictly ascending sequence).
2. **Create the room**: id `<source id, ≤40 bytes>-<8 hex>`, the source name
   and topic, private, owned by the importer — the only member. The source
   default channel becomes the new default (same name); other channels are
   recreated, created by the importer. A channel with `"kind": "people"` is
   restored as the new room's people chat (human-only, invisible to agents);
   `transcript.md` labels its messages "people chat (not visible to agents)".
3. **Artifacts** are recreated version by version (summaries, version authors
   and times kept as attribution; the importer is the artifact's creator).
   Blob versions are re-stored in the blob store and re-verified against
   their `contentHash`; source job/message links are dropped (they name the
   source room's jobs and sequences).
4. **Attachments** are re-uploaded through realtime's regular upload route, so
   the current type allowlist, content sniffing, size cap and content-addressed
   blob store apply. An attachment that route refuses (4xx) is skipped, listed
   in `skippedAttachments`, and dropped from the messages that referenced it.
5. **Messages** are written by realtime's internal, history-only import write
   (`POST /internal/v1/rooms/{roomId}/import`, svcauth-signed, membership
   checked, ≤ 500 rows / 8 MiB per request). Each row:
   - takes the room's next hub sequence (the same per-room counter, seeded from
     the durable max, that live publishes use), so imported rows are monotonic
     and live chat continues after them;
   - keeps its original timestamp and attribution (chat `userId`, agent
     `ownerUserId`/`provider`); room-local references are rewritten (channel
     ids, attachment ids, the agent payload's `workspaceId`/`channelId`);
   - gains the server-stamped marker `payload.imported` =
     `{fromRoomId, fromRoomName, originalSequence, importedAtMs, editedAtMs?}`,
     which history returns and the web renders as an "imported" badge;
   - is stored **without an author**: the author column is the edit/delete
     authorization key, and an identity read from a file never authorizes
     anything — imported rows cannot be edited or deleted as anyone's own;
   - is **never published**: no SSE delivery, no replay buffer entry, no agent
     job, no plugin hook. A bridge therefore cannot drive an agent from an
     imported agent-addressed chat; it is history, read through history.
6. **Provenance** (`room_imports`: source room, exporter, importer, times,
   suggested members) is recorded; the web shows "Imported from <room> on
   <date>" with the suggested members, which owners/admins add through the
   regular add-member flow (a source owner is suggested as admin — ownership
   only moves by an explicit transfer).

A failure after the room was created deletes the room again (best effort).

## Bounds

Package bounds are product limits of the package protocol, not deployment
data:

| Bound | Value | Why |
| --- | --- | --- |
| Uploaded archive | 256 MiB | Matches realtime's generic blob upload cap — the largest single object the platform stores. |
| Sum of declared uncompressed entry sizes | 1 GiB | Zip-bomb guard; `archive/zip` refuses an entry larger than its declared size. |
| `manifest.json` / `artifact.json` | 1 MiB; `attachment.json` 64 KiB | Metadata only. |
| One `messages.jsonl` line | 1 MiB + 64 KiB | Realtime's 1 MiB event payload cap plus framing. |
| Messages | 100 000 | Bounds one import's work. |
| Channels / members / artifacts / versions per artifact | 200 / 5 000 / 1 000 / 1 000 | Bounds one import's work. |
| Artifact version content | 2 MiB inline; blob versions `BNBLOC_ARTIFACT_MAX_BYTES` (default 100 MiB) | The artifact publish caps. |
| Attachment | realtime's attachment cap (25 MiB) and type allowlist | Enforced by the upload route the import uses. |

Export has no total cap: it streams (no temp file, no whole-room buffer), and
an export that fails midway aborts the connection rather than finishing a
well-formed archive that lacks data.
