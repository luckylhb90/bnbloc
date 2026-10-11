# Bounded unread counts and time-ordered entity ids

Status: landed 2026-10-11.

## Unread counts are capped

`UnreadCount` counted every message after the member's read cursor with an
unbounded `COUNT(*)`. `deleted_at_ms` is not in `idx_room_channel_seq`, so
each counted row also read the clustered record. A member who had not opened
a busy room for months made every room-list refresh walk tens of thousands of
rows per room. The clients only ever show "99+" past 99.

- `store.MaxUnreadCount` (100) bounds the count: MySQL counts a
  `LIMIT 100` subquery, the memory store stops at 100. One count reads at most
  100 index entries, whatever the backlog.
- The web shows "99+" (already did) and no longer prints the capped number in
  the room row's title.
- The "New" divider (`unreadBaseline`) is unaffected: past the loaded page it
  already sat above the oldest loaded message.

## Entity ids are time-ordered

Entity primary keys (artifacts, attachments, agent jobs, prompts, channels,
room templates, schedules, leases, quota requests, borrow reservations) were
purely random hex. InnoDB clusters rows by primary key, so random ids insert
at random positions: page splits, half-empty pages and a cold buffer pool for
recent rows.

- `internal/ids.New(prefix)` is the one id generator: prefix + 12 hex digits
  of the Unix millisecond time + 20 hex digits (80 bits) of crypto/rand.
  Ids still sort and insert by creation time, stay unguessable (80 random
  bits), and need no coordination between replicas (no auto-increment).
- Every entity-id call site uses it; the per-file copies are gone. Existing
  ids keep working (ids are opaque strings; no code parses them).
- Secrets and bearer-like values stay fully random: invite codes, claim and
  reply tokens, signing secrets, API and refresh tokens.
