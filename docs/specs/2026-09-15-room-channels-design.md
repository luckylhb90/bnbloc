# Room Channels — Design

Status: **Implemented** (c5d824d and follow-ups; approved 2026-09-15). Owner: enterprise. This document is
the durable design + as-built target for adding channels to rooms.

## Problem

A room today is the canonical workspace scope: members, agent borrows
(`shared_agent_grants` keyed by `room_id`), git/mutagen sync (`rooms.sync_mode`),
and messages (`room_messages` / `agent_messages` keyed by `room_id`) all hang off
it. But a room has exactly one message stream. Teams working a single codebase
want multiple topics (frontend / backend / bug triage) without splitting into
separate rooms — splitting fragments the shared context (each room re-borrows
agents, re-syncs a repo).

## Decision

Do **not** add a `project` layer. The room already *is* the project/workspace.
Add a **channel** layer *inside* a room that partitions only the message stream.
Everything that constitutes shared context stays room-level.

```
tenant → room (workspace: members / agent borrows / git sync / files)
           └── channel (topic-scoped message stream)
```

### Locked decisions

- **Isolation = messages only.** Members, agent borrows, repo/sync, and files
  are room-level and shared across all channels. Switching channel only changes
  the topic, never the context.
- **channel_id = opaque random id; display `name` is mutable.** Matches the
  existing `room_id` style; renaming has zero id side effects.
- **Agent turns anchor to the originating channel.** The turn reads that
  channel's history as context; its output message and audit record land back in
  that channel. Borrow authorization stays room-level; quota/usage stays
  room/tenant-level aggregate.
- **One default channel per room, locked.** Created in the same transaction as
  the room. Cannot be renamed or deleted. No legacy-data migration is in scope
  (greenfield: rooms are created with a default channel).
- **Channel create/delete is lightweight-open.** Any room member may create a
  channel; owner/admin may delete a non-default channel; the default channel is
  immutable.
- **UI = in-room left channel list** (Slack-style), not a new top-level nav.

## Data Model

New table `room_channels`:

| column | notes |
| --- | --- |
| `channel_id` PK | opaque random id (e.g. `ch_` + hex), like `room_id` |
| `tenant_id` | scope + fail-closed tenant isolation |
| `room_id` | owning room |
| `name` | mutable display name (e.g. `frontend`) |
| `is_default` | exactly one true per room; immutable, undeletable |
| `created_by` | subject id |
| `created_at_ms` | metadata |
| `deleted_at_ms` NULL | soft delete, matching `rooms` style |

Indexes: PK `channel_id`; `KEY idx_channel_room (tenant_id, room_id, deleted_at_ms)`.

Altered tables — add `channel_id VARCHAR(64) NOT NULL` to:

- `room_messages` — new PK/pull index `(room_id, channel_id, seq)` so history is
  fetched per channel. `seq` remains room-scoped monotonic (a single room
  sequence keeps cross-channel ordering coherent and avoids a per-channel
  counter).
- `agent_messages` — add `channel_id`; pull index `(room_id, channel_id,
  agent_session_id, seq)`.

Unchanged (this is the point): `rooms`, `room_members`, `shared_agent_grants`.

`schema.sql` gets the new table + columns; a forward migration adds them for
existing deployments (create table + `addColumnIfMissing`), and room creation
inserts the default channel transactionally.

## API Contract

Change the OpenAPI spec first (repo hard rule), then
implement. All channel endpoints reuse the existing room-membership gate.

New (room-member authorized):

```
GET    /v1/rooms/{roomId}/channels                  list channels (incl. is_default, unread)
POST   /v1/rooms/{roomId}/channels                  create {name} — any member
PATCH  /v1/rooms/{roomId}/channels/{channelId}      rename {name} — owner/admin or creator; default -> 409
DELETE /v1/rooms/{roomId}/channels/{channelId}      delete — owner/admin; default -> 409
```

Existing endpoints gain an optional `channelId` (query or body). Absent =>
the room's default channel, so no caller breaks:

- `POST /v1/rooms/{roomId}/chat` — message lands in the given channel.
- `GET  /v1/rooms/{roomId}/history?channelId=` — per-channel history.
- `GET  /v1/rooms/{roomId}/events?channelId=` — SSE filtered to the channel.
- `POST /v1/rooms/{roomId}/read`, `/typing` — carry `channelId`.
- shared-agent record/turn — body carries `channelId`; output posts there.

## Realtime

- Publish events with their `channel_id`.
- `/events` (SSE) accepts `channelId` and filters **server-side**
  before pushing (not client-side) to save bandwidth. `typing` / read receipts
  are likewise channel-scoped.
- Unread refines from room-level to **room→channel** granularity:
  `/v1/rooms-unread` returns per-channel counts so each channel shows its own
  badge. Room-level unread is the sum.

## Agent Turn Routing

- Borrow authorization unchanged (room-level `shared_agent_grants`).
- A turn request carries `channelId`. The agent's conversation context is that
  channel's history; the produced message and the `AuditRecord` are written to
  that channel.
- Quota enforcement and usage aggregation stay room/tenant-level (not
  per-channel), reusing the existing ceilings.

## UI (in-room left channel list)

- `RoomView` gains a narrow left channel rail: `#general` (default, pinned top)
  then other channels, each with its own unread dot; a `+ New channel` affordance
  at the bottom (visible to any member).
- Selecting a channel scopes the message list, composer, and agent turns to it.
- Per-channel hover/context actions: rename, delete (shown per permission).
- Built only from shared UI system components + semantic tokens; user-facing
  copy through the i18n layer; Chinese copy avoids the trailing 。 per repo rule.

## Boundaries / Non-goals

- No `project` entity. No per-channel membership or private channels (isolation
  is messages-only). No per-channel agent borrow or quota. No message migration.
- Windows impact: none — this is HTTP/DB/UI only, no path/process/shell surface.

## Testing

- Store: default channel created with room; channel CRUD; default channel
  rename/delete refused; messages fetch scoped by channel; cross-tenant
  isolation preserved (channel of tenant A invisible to tenant B).
- API/gateway: `channelId` omitted routes to default; member may create, only
  owner/admin may delete, default delete → 409.
- Realtime: an SSE subscriber on channel A does not receive channel B events.
- Agent: a turn with `channelId` posts its output to that channel; audit record
  carries the channel.
- UI: smoke via browser — create channel, switch, send message lands in the
  right channel, unread badge per channel.

## Rollout

1. OpenAPI contract change.
2. Store: table + columns + migration + default-channel-on-room-create.
3. room service: channel CRUD + `channelId` on chat/history/read/typing.
4. realtime: channel-filtered events + per-channel unread.
5. shareagent: `channelId` on record/turn + channel on audit.
6. Web: channel rail in RoomView + api client.
7. Docs: update room/architecture docs; this spec becomes the as-built record.
