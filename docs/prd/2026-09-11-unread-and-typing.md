# Unread counts and typing indicator

Status: done
Commits: efe7131 (unread), 58bdbf3 (typing)

Two live-room UX features. The unread seq-space defect found during rollout is
recorded separately in `docs/bugs/2026-09-11-unread-read-cursor-poisoned.md`.

## Unread counts per room (`efe7131`)

Intent: a member in multiple rooms should see which have unseen messages.
Design:
- store: `room_read_state` (room, user, last_read_seq) + `MarkRead` /
  `UnreadCount` / `MaxRoomSeq`.
- realtime: `POST .../read` advances the caller's last-read seq (monotonic);
  `GET /v1/rooms-unread?rooms=` returns per-room counts (membership-checked).
- web: MyRooms shows an unread badge per room; RoomView marks the room read as
  the feed's latest sequence advances (debounced).
- Durability: the hub seeds its per-room sequence from the persisted max on
  restart (`Hub.SeedSeq → Chat.MaxRoomSeq`), so sequences continue monotonically
  and a restart cannot make new messages look already-read.

Acceptance: unread 0 → N after N new messages → 0 after reading, across a
realtime restart; badge shows on the My rooms row.

## Human typing indicator (`58bdbf3`)

Intent: show "X is typing…" between people.
Design: the composer emits a throttled (≤1/2s) `POST .../typing`; realtime
publishes a transient `presence_typing` event that is NOT persisted (excluded
from `persist()`, which stores only chat/agent messages); other members show a
hint above their composer that auto-expires after 4s.

Acceptance: a member's typing reaches other members' streams; history excludes
it (only chat_message/agent_messages persist).
