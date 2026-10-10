# Room Plaza, Visibility, and UI Redesign — Design

Status: implemented (6143928; approved 2026-09-09). Scope: bnbloc-room service, gateway routing, web
client (`services/gateway/webui/static/index.html`). Single implementation plan.

## Goal

1. **Room Plaza**: every room is discoverable and searchable in a plaza.
2. **Visibility**: a room is `shared` (join without an invite) or `private`
   (invite code required). The owner can toggle it at any time.
3. **Differentiated room lists**: the user's rooms are split into rooms they
   created vs rooms they joined, and created rooms are tagged `shared`/`private`.
4. Whole-page UI redesign: cleaner layout, sensible spacing/typography, still
   attractive; mobile responsive retained.

## Decisions (locked)

- New rooms default to **private**.
- Plaza lists **all** rooms in this bnbloc instance (single enterprise tenant).
  Private rooms appear **locked**: only `roomId`, `name`, `description`, and
  `locked: true` are exposed — never member lists, activity, or counts. `shared`
  rooms may be joined directly; `private` requires an invite code.
- Room lifecycle: members may **leave**; the owner may **delete** (soft, sets
  `deleted_at_ms`, cascades membership/invite cleanup) or **transfer** ownership
  (old owner becomes `member`). The owner cannot `leave` without transfer/delete.

## Data model

`store.Room` gains two fields:

- `Visibility Visibility` — `"shared" | "private"`; default `private`.
- `Description string` — short plaza topic line (may be empty).

`Visibility` is a new string type with `NormalizeVisibility` (unknown → private).

Touch points (all three must change together):

- `internal/store/room.go`: `Room` struct, `Visibility` type, `MemoryRoom`.
- `internal/store/mysql_room.go`: all affected queries.
- `deploy/schema.sql`: `rooms` gains `visibility ENUM('shared','private') NOT
  NULL DEFAULT 'private'` and `description VARCHAR(500) NOT NULL DEFAULT ''`.
  Backfill of existing rows is implicit via column defaults.

### RoomStore interface additions

```
SearchRooms(query string, limit, offset int) ([]RoomWithCount, int, error)
UpdateRoom(roomID string, patch RoomPatch) error   // name/description/visibility
DeleteRoom(roomID string) error                     // soft delete + cascade
TransferOwner(roomID, newOwner string) error        // old owner -> member
RoomsForUserDetailed(userID string) ([]RoomMembershipDetail, error)
MemberCount(roomID string) int
```

- `RoomWithCount` = `Room` + `MemberCount int`.
- `RoomPatch` = pointers for `Name`, `Description`, `Visibility` (nil = unchanged).
- `RoomMembershipDetail` = `{Room, Role, IsOwner, MemberCount}`.
- `SearchRooms` matches `name`/`description` substring (case-insensitive),
  excludes soft-deleted, orders newest-first, returns total for pagination.

## Room API (bnbloc-room)

| Method + path | Auth | Behavior |
| --- | --- | --- |
| `GET /v1/rooms?q=&limit=&offset=` | any authed | Plaza list. Each row: `roomId, name, description, visibility, createdBy, createdAtMs, memberCount, locked`. For `private`, redact to `{roomId, name, description, visibility:"private", locked:true}`. Returns `{rooms, total, limit, offset}`. |
| `POST /v1/rooms/{id}/join` | any authed | Join a `shared` room (self-add `member`, idempotent). `private` → 403 (must use invite). Returns `{roomId, role}`. |
| `PATCH /v1/rooms/{id}` | owner | Patch `name`/`description`/`visibility`. |
| `POST /v1/rooms/{id}/leave` | member (not owner) | Remove caller's membership. Owner → 409 (transfer/delete first). |
| `POST /v1/rooms/{id}/transfer` | owner | Body `{userId}` (must be a member). New owner = `owner`, old = `member`. |
| `DELETE /v1/rooms/{id}` | owner | Soft-delete, remove memberships. |
| `GET /v1/users/{id}/rooms?detail=1` | self | Rich rows: `{roomId, name, visibility, role, isOwner, memberCount}`. Without `detail`, unchanged `{rooms: [...ids]}` (login path). |

Invariants: private rooms never expose members/activity via plaza; `join`
refuses private; mutating routes (`PATCH`/`DELETE`/`transfer`) are owner-only;
`leave` refuses the owner.

## Gateway

- `route()` already sends `/v1/rooms*` to the room backend; the new paths match
  the existing `/v1/rooms` prefix — no route change except confirming
  `join/leave/transfer` are not misrouted to realtime/activity/shareagent
  (those match on `/events`,`/chat`,`/agent-activity/`,`/shared-agents/`, which
  do not collide).
- **Scope exemption**: `roomScopeRe` blocks `/v1/rooms/{id}/...` for non-members.
  `POST /v1/rooms/{id}/join` MUST be exempt (the caller is joining, not yet a
  member). `GET /v1/rooms` (plaza) has no `{id}` segment, so it is not scoped.
- **Token refresh**: joining/redeeming adds a membership the current session
  token lacks. The web client calls the existing `reauth()` after a successful
  join/redeem to re-mint a token carrying the new room scope, so SSE/chat work
  immediately.

## UI redesign

Single-file rewrite of `webui/static/index.html`. Structure:

- **Topbar**: brand, global search field (routes to Plaza search), account menu.
- **Left nav**: Plaza | My Rooms. My Rooms shows two groups:
  - **Created by me** — each row tagged `shared`/`private` with a toggle.
  - **Joined** — each row with a Leave action.
- **Center**:
  - Plaza: responsive card grid; each card shows name, description, member
    count, and either a **Join** button (shared) or a **lock + invite input**
    (private). Server-side search via `q`.
  - In-room: live activity feed + chat composer (unchanged behavior; chat is the
    single agent input per the room-claimer design).
- **Right**: members, room settings (visibility toggle, rename, edit
  description, invite, transfer, delete/leave), My local agent, Run agent.
- **Mobile**: retain the `<=720px` stacked layout; nav collapses to a top row.

Visual language: consistent dark palette already in the file, tightened spacing
scale, clearer type hierarchy, card component reused across plaza and lists. No
new external dependencies; no fabricated QR/asset.

## Testing / verification

- Go unit coverage for new store behavior on `MemoryRoom`: visibility default,
  search substring + redaction rule, join refuses private, leave refuses owner,
  transfer swaps roles, delete hides from plaza and `GetRoom`.
- End-to-end against the running stack: create (default private) → appears
  locked in plaza → toggle to shared → joinable by a second user → second user
  leaves → owner transfers → delete removes from plaza.
- Browser visual check of the redesigned layout (desktop + narrow viewport).

## Local agent runtime install hint

The web client needs a local agent runtime (the "local proxy" that launches
codex / claude CLIs) to run the user's OWN agent. A phone never needs it (its
messages are claimed by the user's desktop runtime).

Auto-detection (client-side, zero config):

- **Mobile** (narrow viewport / mobile UA): show a one-line note — "No install
  needed on mobile; just chat and your desktop takes over" — never a download.
- **Desktop browser + local runtime not detected**: `loadAgentTargets()` already probes
  the gateway proxy `GET /v1/local-agent/targets`; an empty/502 result means no
  reachable local runtime. Show an install card in the "My local agent" section
  with the correct download link chosen from `navigator.platform`, plus a
  "Re-check" button that re-runs detection.
- **Detected**: no hint; show the agent picker as today.

Detection MUST go through the gateway proxy, not a direct browser fetch to
`http://127.0.0.1:4545`, because an https gateway blocks mixed-content requests
to a loopback http origin.

Download links come from the deployment's configured desktop releases, never
from hardcoded URLs.

## Out of scope

- Per-room roles beyond existing owner/admin/member/visitor.
- Moderation, reporting, room categories/tags, pagination cursors (offset paging
  is sufficient at this scale).
