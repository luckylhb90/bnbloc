# Room Channels

A room is the workspace scope (members, agent borrows, git/mutagen sync, files).
A channel is a topic-scoped message stream **inside** a room. Channels partition
only messages; every other room property is shared across all channels. Design:
`docs/specs/2026-09-15-room-channels-design.md`.

## Model

```
tenant → room (workspace) → channel (message stream)
```

- Every room has exactly one **default channel** (`general`, `is_default`),
  created in the same transaction as the room (`RoomStore.
  CreateRoomWithOwnerAndChannel`). It can be neither renamed nor deleted.
- `channel_id` is an opaque random id (`ch_` + hex); the display `name` is
  mutable. Room ids may be reused across tenants, so every channel operation is
  scoped by the context tenant (fail closed).
- `seq` stays **room-scoped monotonic** — one counter per room keeps
  cross-channel ordering coherent. `channel_id` is a filter column on
  `room_messages` / `agent_messages`, never part of the counter.
- Agent borrow authorization stays room-level (`shared_agent_grants` untouched);
  quota enforcement and usage stay room/tenant-level aggregates.
- A channel has a `kind`: `''` (ordinary) or `people` — the room's **people
  chat**, a human-only side conversation agents never see (one per room; id
  derived from tenant+room so concurrent lazy creates converge). It is never
  listed by `GET .../channels`, never delivered to an `allChannels=1` stream
  without `includePeople=1`, never addressed to an agent, and never fed to
  plugin hooks (`docs/prd/2026-10-08-ui-nav-composer-people-chat.md` §3).

## API

All channel endpoints require room membership (same gate as the other room
endpoints). `channelId` on existing endpoints is optional and defaults to the
room's default channel, so pre-channel callers are unaffected.

```
GET    /v1/rooms/{roomId}/channels                  list (default first)
POST   /v1/rooms/{roomId}/channels                  create {name}     — any member
PATCH  /v1/rooms/{roomId}/channels/{channelId}      rename {name}     — owner/admin or creator; default/people → 409
DELETE /v1/rooms/{roomId}/channels/{channelId}      delete            — owner/admin; default/people → 409
GET    /v1/rooms/{roomId}/people-chat               the people chat   — 404 until first opened
POST   /v1/rooms/{roomId}/people-chat               open (create once) — any member, idempotent
```

Existing endpoints gained an optional channel selector:

- `POST /v1/rooms/{roomId}/chat` — body `channelId`; the message lands in that
  channel (absent → default).
- `GET  /v1/rooms/{roomId}/history?channelId=` — per-channel history.
- `GET  /v1/rooms/{roomId}/events?channelId=` — the SSE stream (the only event
  lane; the unused WebSocket lane was removed) filters **server-side**: a subscriber on channel A never receives channel B's
  events. Events with an empty `channelId` (presence, roster, agent state) are
  room-wide and always delivered.
- `POST /v1/rooms/{roomId}/read`, `/typing` — body `channelId`; read cursors are
  per (room, channel, user).
- shared-agent borrow record — body `channelId`; the audit record carries it so
  an admin sees which topic drove a borrow turn.

## Read cursors and unread

`GET /v1/my/unread` answers `{ "rooms": n, "more": bool }`: how many of the
caller's rooms (in the tenant the request is signed for) have a live unread
message in a live channel, counted to 100 and over at most 500 rooms (`more`
says the number is a floor). It is the cheap read for an account no window
shows; `/v1/rooms-unread` below is the per-room, per-channel detail.

`room_read_state` is keyed `(room_id, channel_id, user_id)` — one cursor per
channel. `/v1/rooms-unread` is additive:

```json
{ "unread":   { "<roomId>": <total across all channels> },
  "channels": { "<roomId>": [ { "channelId": "…", "count": 3 } ] } }
```

The legacy `unread` map keeps its old shape (so older clients keep working) but
now sums every channel; `channels` lists only channels with unread > 0,
default-first. The web channel rail consumes the per-channel list.

## Data flow (chat message with channel)

1. Client posts `/chat` with `channelId` (the rail always sends it).
2. Realtime resolves the channel: explicit id passes through; absent → the
   room's default; a room without a default (broken invariant) falls back to the
   legacy `""` stream with a warning log.
3. The hub publishes `wire.Event{ChannelID: …}`; SSE/WS subscribers receive it
   only if it matches their subscribed channel or is room-wide.
4. `persist` stamps `room_messages.channel_id`; the agent borrow record (if the
   message drove an agent) stamps `shared_agent_audit.channel_id`.

## Semantics of the empty channel id

`""` means different things by call site — deliberate, documented on the
`ChatStore` interface:

- `RoomHistory` / `MarkRead` / `UnreadCount`: `""` addresses the legacy
  default/empty channel **(not a wildcard)** — it matches only rows stored with
  an empty channel id.
- `MaxRoomSeq`: `""` means **room-wide** (sequence allocation must read the
  room's single counter).

## Store

`ChannelStore` (`internal/store/channel.go`): `PutChannel`, `GetChannel`,
`ListChannels` (default first, then created_at), `DefaultChannel`,
`RenameChannel` (liveness-conditional; resurrecting a soft-deleted channel is
impossible), `DeleteChannel(atMs)` (soft delete). Memory and MySQL impls; both
tenant-scoped. MySQL room creation, owner membership, and default channel are
one `sql.Tx` — a room can never exist without its default channel.

Migrations: `2026_10_room_channels` (table + message columns + index),
`2026_10_room_channels_read_state` (per-channel cursor PK),
`2026_10_room_channels_audit` (audit channel column).
