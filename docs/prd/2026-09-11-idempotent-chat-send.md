# Feature: idempotent chat send (client message id)

Status: done
Commits: (this change)

## Intent

A network retry or double-tap on send could post the same chat message twice —
there was no dedup on the chat write path (only agent messages had a
`client_submit_id`). Make chat sends idempotent.

## Design

- Client generates a `clientMsgId` (UUID) per send and includes it; a retry of a
  failed message reuses the same id (stored on the optimistic feed item).
- `room_messages` gains a nullable `client_msg_id` column with a unique
  `(room_id, client_msg_id)` key. `ChatStore.LookupClientMsg(room, id)` returns
  the stored sequence.
- realtime `handleChat`: if the id was already stored, return the original
  sequence without re-publishing; otherwise publish and persist with the id.
- Live DB migrated with `ALTER TABLE room_messages ADD COLUMN client_msg_id …`
  (the `CREATE TABLE IF NOT EXISTS` schema does not alter existing tables).

## Verification

Same clientMsgId × 3 → identical sequence, stored once; no id → each send stored
separately (unchanged). A retry cannot double-post.
