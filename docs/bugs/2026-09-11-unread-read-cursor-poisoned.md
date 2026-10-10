# Bug: unread counts never show (poisoned read cursor)

Status: done
Commits: 07c4d81

## Repro

Send messages to a room, then open **My rooms** — the unread badge on the room
row stays absent (count effectively 0) no matter how many unseen messages exist.

## Root cause

Agent replies and human chat were sequenced in the feed from **two different
sequence spaces**:

- chat used the hub's per-room sequence (small, e.g. ~57),
- agent replies used the local core's per-message sequence (large, e.g. ~85000).

The client's `markRead` records the feed's max sequence as `last_read_seq`, so
it stored ~85000. Every real room message (hub seq ~57) then fell below the
cursor and was treated as already-read — unread stuck at 0.

## Fix

- `normalizeRoomEvent` sequences agent messages in the **room (hub) space**
  using the envelope sequence, offset by in-batch index to stay distinct and
  ordered.
- `markRead` floors the sequence before sending.
- Cleared the poisoned `room_read_state` rows.

## Verification

Backend: jack reads to latest → unread 0; maya sends 2 → unread 2. UI: the
My rooms `test` row shows a numeric unread badge.
