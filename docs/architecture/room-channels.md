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

## Setup (docs/prd/2026-10-28-channels-and-threads.md, slice 1)

A channel holds its own setup: a **purpose** (≤ 250 characters) and
**rules** (≤ 4,000), a **section** (`sectionId`, `""` = the room's top
level) and a **position** in it, an **owner** (its creator by default), an
**archive** mark and a **settings** document (`wire.ChannelSettings`, the
`settings_json` column):

| Setting | Values | Enforced by |
|---|---|---|
| `defaultAgent` | `{ownerId, core, provider}` or none | nothing yet: what the composer proposes (C8) |
| `whoPosts` | `""` everyone, `"admins"` the room's owner and admins | realtime's channel gate |
| `noAgents` | false (agents admitted) / true | `store.ChannelAdmitsAgents` (below) |
| `slowModeSeconds` | 0 off; the owners' value, no default (C22) | realtime's channel gate |
| `agentParticipation` | `""` when mentioned, `"questions"`, `"threads"` | stored only (C19) |

- **Who changes it** (`mayManageChannel`, `services/room/channels.go`): the
  room's owner and admins, the channel's owner, the owners of its section
  (C10). The default channel keeps its name, is never archived or deleted
  and never bars agents; the people chat is not configurable.
- **Archived** channels are read-only (every post refused,
  `channel_archived`), leave the default listing (`?archived=1` lists them),
  keep their history and stay in the agents' listing; a restore reopens them.
- **Sections** (`room_sections`) are named groups with owners and rules.
  A section is open or members-only (below). Deleting one moves its channels to the
  top level. Who makes one and who places a channel where is the room's
  **channel creation** policy (`anyone` default, `sections`, `admins`, C15):
  `mayCreateSection` and `mayPlaceChannel` (the room's owner and admins
  anywhere; a member at the top level under `anyone`, and in a section they
  own).
- **Members-only sections** (slice 2; C11-C14, C17): `section_members`
  holds who belongs, `section_join_requests` who asked. Join policy
  `invite` (default: the section's owners add members; nobody else sees it),
  `ask` (its name is shown for joining; its owners approve) or `open` (its
  name is shown; any member joins). The creator is the first owner and
  member. The room's owners and admins see every members-only section's
  name and members and read it only by joining. Their join is announced
  (`section_membership`, `admin`) and audited (`AuditKindSection`).
  Visibility is fixed at creation. The agent door never opens the
  membership routes.
- **Who may read a channel** has one rule, `store.MayReadChannel`
  (`internal/store/channel_access.go`): the top level and open sections for
  every reader in the room, a members-only section for its members, and
  for an agent also `ChannelAdmitsAgents`. The reader is a person. An agent
  reads as the person it works for: the bridge reads its listing and
  history with the turn's job (`?job=`), which the gateway proxies as the
  job's requester. Each service asks it through one owner, and every read
  path asks:
  - room service (`readerAccess`, `roomArtifact`, `newReadableIn`):
    - the channel listing (`?agents=1` included) and directory;
    - a channel's own routes (404 when not readable);
    - sections (`knownSections`, `liveSection`);
    - placing or moving a channel into a members-only section;
    - deliverables: list, anchors, detail, versions, content, files,
      thumbnails, live state, share-link creation, My apps;
    - the handoff export (filtered by the exporter's access).
  - realtime (`channelAccess`, `memberMayRead`):
    - the event stream, re-scoped on `channel_updated` and
      `section_membership`; deliverable events name their channel and
      reach other channels' streams only for its readers
      (`wire.CrossChannelKind`);
    - history, the consented history, and revisions (and their
      `retainedFrom`);
    - posting (`PostChat`: typed, scheduled, dispatched) and a post's file
      reference;
    - edit, delete, tasks and unread;
    - schedules (creating one in a channel, and listing);
    - global search (messages and deliverables);
    - mention notifications (`notify.Observer.MayRead`), joining by
      mention, and plugin hooks.
  - activity (`channelReaders`): run listings and records, and the room's
    job rows.

  `TestChannelReadsAskTheRule` (`internal/archtest`) fails when a
  function in these services reads channel content without asking the rule
  or an exemption that says why.
- **Following** (`channel_follows`, C18): what a member's rail shows, never
  access. Posting and being mentioned join a channel (realtime), so does
  creating one; a new member joins the room's **recommended channels**
  (`addMember`/`redeemInvite`). Each member stars and hides channels for
  themselves; the directory lists every open channel with its followers.
  Every member is in the default channel whether or not a follow row exists
  (`channelView`), and cannot leave it.
- **Personal groups** (`channel_groups`, C24): a member's own folders of
  their rail, listed only to them (`groups` of the channel list). A follow's
  `group_id` puts a channel in one (`PUT .../follow {groupId}`, only the
  caller's own group; `""` takes it out); the channel's section is unchanged
  for everyone. Deleting a group takes its channels out of it in the same
  step; leaving a channel drops its group with the follow. The rail lists
  the top level, then the member's groups, then the sections
  (`railGroups`); a folded group or section shows only the open channel and
  those with unread (`foldedChannels`, one rule for both).
- **The room's rules** (≤ 8,000) carry a version a change of text bumps; when
  the room **requires agreeing** (C16, off by default) a member who has not
  agreed to the current version is refused a post
  (`rules_agreement_required`) until `POST .../rules/agree {version}`.
- **The channel gate** (`admitPost`, `services/realtime/channel_gate.go`) is
  the one owner of "may this member post here now", asked by `PostChat` for
  every post (typed, scheduled, dispatched), after the membership check and
  before anything is stored: archived, admins-only, rules agreement, then the
  slow-mode claim (`ClaimChannelPost`: one write that refuses within the
  interval with `retryAfterMs` and a `Retry-After` header, and records the
  post, which joins). Each refusal has a stable code (`wire.Channel*`,
  `wire.RulesAgreementRequired`).
- **Agents admitted** has one rule, `store.ChannelAdmitsAgents`: never the
  people chat, never a channel whose settings say `noAgents`. Every agent
  path asks it (agent recipients, agent replies in realtime and activity,
  the handoff import, plugin hooks, agent streams, history and revisions
  for agent readers). A setting can change, so every change to a channel
  or section publishes the room-wide `channel_updated` event (after the
  store write); realtime's agent stream filter forgets its memoized verdicts
  on it (failing closed on a lookup error), and an agent host's one-channel
  stream ends when its channel bars agents. The bridge's channel listing
  (`roomlog.AgentChannelList`) asks `GET .../channels?agents=1`, which
  leaves those channels out. Between the store write and the event's
  delivery, an agent stream may still receive one event of the channel
  (the memo's window).
- **By talking**: `room_setup` action `channel` makes a channel or changes
  one (its fields, settings, archive, restore) as the member who asked,
  through the agent door; `set` takes the room's rules and recommended
  channels (`docs/architecture/room-tools.md`). Deleting a channel, a
  channel's owner, sections and following stay people's acts.
- **The rules' mirror in the web**: `web/src/lib/channelRules.ts` shows a
  control only where the room service allows the act; it is held to
  `services/room/testdata/channel_rules.json`, which the Go owners
  (`mayManageChannel`, `mayPlaceChannel`, `mayCreateSection`,
  `mayManageSection`, delete) are tested against too. The server stays the
  authority: a refusal's code (`wire.Channel*`, `wire.RulesAgreementRequired`)
  is shown in words on the control that acted.

## API

All channel endpoints require room membership (same gate as the other room
endpoints). `channelId` on existing endpoints is optional and defaults to the
room's default channel, so pre-channel callers are unaffected.

```
GET    /v1/rooms/{roomId}/channels                     wire.ChannelList: channels (not archived; ?archived=1 the archived;
                                                       ?agents=1 the agents may read) + sections, with the caller's joined/starred/hidden
GET    /v1/rooms/{roomId}/channel-directory            every open channel with its followers
POST   /v1/rooms/{roomId}/channels                     create {name, purpose?, sectionId?} — where mayPlaceChannel lets the caller
PATCH  /v1/rooms/{roomId}/channels/{channelId}         name, purpose, rules, sectionId, position, ownerId, settings — mayManageChannel
POST   /v1/rooms/{roomId}/channels/{channelId}/archive — mayManageChannel; default/people → 409
POST   /v1/rooms/{roomId}/channels/{channelId}/restore — mayManageChannel
DELETE /v1/rooms/{roomId}/channels/{channelId}         delete — owner/admin; default/people → 409
PUT    /v1/rooms/{roomId}/channels/{channelId}/follow  follow {starred?, hidden?, groupId?} — any member (their own)
DELETE /v1/rooms/{roomId}/channels/{channelId}/follow  unfollow (not the default channel → 409)
POST   /v1/rooms/{roomId}/channel-groups               create {name, position?} — any member (their own)
PATCH  /v1/rooms/{roomId}/channel-groups/{groupId}     name, position — their own (another's → 404)
DELETE /v1/rooms/{roomId}/channel-groups/{groupId}     its channels leave the group — their own
POST   /v1/rooms/{roomId}/sections                     create {name, rules?, position?, visibility?, joinPolicy?} — mayCreateSection
PATCH  /v1/rooms/{roomId}/sections/{sectionId}         name, rules, position, owners — mayManageSection
DELETE /v1/rooms/{roomId}/sections/{sectionId}         its channels move to the top level — mayManageSection
POST   /v1/rooms/{roomId}/sections/{sectionId}/join    join (open), ask (ask); the room's owner/admin joins any — members-only
DELETE /v1/rooms/{roomId}/sections/{sectionId}/join    leave (not the only owner)
POST   /v1/rooms/{roomId}/sections/{sectionId}/members {userId}: add, or approve a request — mayManageSection
DELETE /v1/rooms/{roomId}/sections/{sectionId}/members/{userId}  remove (not an owner) — mayManageSection
GET    /v1/rooms/{roomId}/sections/{sectionId}/requests          open requests — mayManageSection
DELETE /v1/rooms/{roomId}/sections/{sectionId}/requests/{userId} decline — mayManageSection
PATCH  /v1/rooms/{roomId}                              rules, requireRulesAgreement, recommendedChannels, channelCreation — owner/admin
GET    /v1/rooms/{roomId}/rules                        the rules, their version, required, the caller's agreed version
POST   /v1/rooms/{roomId}/rules/agree                  {version}: the current version only (409 rules_changed)
GET    /v1/rooms/{roomId}/people-chat                  the people chat   — 404 until first opened
POST   /v1/rooms/{roomId}/people-chat                  open (create once) — any member, idempotent
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

## Stream scopes

A room stream (`GET /v1/rooms/{id}/events`) is one of three scopes:

- **one channel** (`channelId`, default the room's default channel): that
  channel's events and the room-wide ones (the people-chat panel, the
  thread panel);
- **all channels** (`allChannels=1`): every channel the subscriber may
  read, agents' streams without the channels agents are not admitted to
  (the bridge);
- **lanes** (`lanes=1&open=<id>,…`, a person's only; the web room view):
  full events for the open channels, the room lane always, and one
  coalesced `channel_activity` a second for every other channel it may
  read (`wire.StreamLane`, docs/prd/2026-10-28-channels-and-threads.md
  §2.4). Its first event, `stream_opened`, names it; `POST
  .../events/scope {streamId, open}` moves it to other open channels
  without reopening.

Every scope asks `store.MayReadChannel` of every channel-scoped event.

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
`ListChannels` (default first, then position, then created_at; archived
channels included, the callers filter), `DefaultChannel`, `UpdateChannel`
(a patch, liveness-conditional; resurrecting a soft-deleted channel is
impossible), `SetChannelArchived` (archive keeps its first time; nil
restores), `DeleteChannel(atMs)` (soft delete), `ChannelAdmission` (kind and
`noAgents`, deleted rows included, read failures reported); sections
(`PutSection`, `GetSection`, `ListSections`, `UpdateSection`,
`DeleteSection`, which moves the section's channels to the top level in the
same transaction); follows (`FollowChannel`, `UnfollowChannel`,
`ChannelFollows`, `ChannelFollowerCounts`, `ClaimChannelPost`); a member's
groups (`PutChannelGroup`, `ChannelGroups`, `UpdateChannelGroup`,
`DeleteChannelGroup`, which takes the group's channels out of it in the
same transaction). The room
store holds the room's rules, version, agreement switch, recommended
channels and creation policy, a member's `RulesAgreedVersion` and
`AgreeRoomRules` (the current version only, in one statement). Memory and
MySQL impls with one contract suite; both tenant-scoped. MySQL room
creation, owner membership, and default channel are one `sql.Tx` — a room
can never exist without its default channel.

Migrations: `2026_10_room_channels` (table + message columns + index),
`2026_10_room_channels_read_state` (per-channel cursor PK),
`2026_10_room_channels_audit` (audit channel column), `2026_10_channel_setup`
(the setup columns, the room's rules columns, the member's agreed version,
`room_sections`, `channel_follows`; an existing channel's owner is filled
from its creator once), `2026_10_channel_groups` (`channel_groups`,
`channel_follows.group_id`), `2026_10_section_members`
(`section_members`, `section_join_requests`).
