# Message delete (author soft-delete)

Status: done (edit deferred)
Commits: (this change)

## Intent

Let a member delete a message they posted; it disappears from the room for
everyone and from history.

## Design

- store: `room_messages.deleted_at_ms` (soft delete). `DeleteRoomMessage(room,
  seq, requester, atMs)` sets it **only when the requester authored the row**
  (enforced in the SQL WHERE / the memory author check); returns deleted=false
  otherwise. `RoomHistory` excludes deleted rows.
- realtime: `DELETE /v1/rooms/{roomId}/messages/{seq}` — member-scoped; the store
  enforces authorship (403 for non-author/missing). On success it publishes a
  transient `message_deleted` event (not persisted) carrying the sequence.
- web: SSE handles `message_deleted` and removes the row from the feed for all
  clients; the author sees a trash button on their own sent messages.
- Live DB migrated with `ALTER TABLE room_messages ADD COLUMN deleted_at_ms`.

## Scope

Author-only. Owner/admin moderation-delete would require realtime to know room
roles (a cross-service role lookup) and is left as a follow-up. **Edit** is also
deferred — it needs content mutation + an edited-message broadcast + history
reconcile; deletion is the higher-value, lower-risk half.

## Verification

Non-author → 403; author → 200; deleted message excluded from history; live
clients drop it via the broadcast.
