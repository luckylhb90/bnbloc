# Navigation, composer and people-only chat redesign (2026-10-08)

Status: landed

Decisions (2026-10-08, product owner): one people chat per room (no 1:1 DMs);
wide screens get a right-side docked panel with a desktop pop-out window,
mobile a full-screen sheet; inline people-only messages stay visible to
agents as context (they just don't reply).

## 1. Sidebar: core on top, everything else in a bottom menu

The sidebar grew to ~11 entries. Core = what a member does every day: find
and work in rooms with agents.

- **Top (always visible):** Search (⌘K), My rooms (unread badge + room list),
  Plaza (discover/join rooms), Agents (my agents / cores).
- **Bottom:** the existing notices (desktop update, connect desktop, new web
  version) stay above a single **account button** (avatar + name). It opens a
  menu grouped as:
  - *Work:* Library, Team (runs, usage, budgets)
  - *Workspace:* Components, Organization / Platform (admins only),
    Organizations directory
  - *Me:* Settings, Download desktop app (Releases), Sign out
  - footer: version stamp
- When the active view lives in the menu, the account button shows it as
  active (and the menu highlights the entry), so the user always sees where
  they are.
- Collapsed rail and mobile drawer use the same split. Keyboard accessible
  (menu button, arrow keys, Esc).

## 2. Composer: flat, full-bleed

The composer is a rounded card inside a padded strip, which wastes space
on phones. New layout: no outer padding, no card border/radius, no reading
measure; one top border line, the textarea spans the full width, the toolbar
sits directly under it. Only the safe-area inset (iPhone home indicator)
remains. Same on all widths. The agent picker label is shortened (provider +
owner initial / avatar) so the toolbar fits on 375px.

## 3. People-only: two modes

Facts today: every human message in a channel is written into the agents'
room context (`.room/live.md`); "People only" only means *no agent replies*.

### Mode A — inline people-only (keep, clarify)

Messages in the main timeline addressed to people. The agent does not reply
but **can read them as context** (that is the point: discuss next to the
agent's work, then ask it). Changes:
- The toolbar toggle becomes an explicit "To: Agent ▾ | People" segment; in
  People mode the placeholder reads "Message people (agent won't reply)".
- People-only messages get a subtle marker in the feed so readers know no
  reply is coming.

### Mode B — People chat (new): a private side conversation, no agent

A separate conversation attached to the room that agents never see.

- **Data:** a room channel of a new kind `people` (reuses channels: history,
  unread, search, edit/delete, attachments, mentions). Created lazily, one per
  room.
- **Hard guarantees (server-side, not UI):**
  - realtime rejects any message with an agent recipient in a people channel;
  - people-channel events are never delivered to agent subscriptions (the
    bridge's stream) and never written into agent room context;
  - plugin hooks (`chat_message.v1`) do not fire for it;
  - room handoff export includes it, marked as people chat.
- **UI:**
  - room header "People chat" button with its own unread badge;
  - desktop/wide: a right-side docked panel next to the agent timeline
    (resizable); mobile: a full-screen sheet;
  - desktop app: "Pop out" opens it in its own OS window;
  - message action "Ask the agent about this" copies a people-chat message
    into the main composer as a quoted reference — an explicit, user-driven
    bridge from private talk to the agent.
- Notifications: mentions in people chat notify like any mention, deep link
  opens the panel at the message.

## Implementation notes — §3 (2026-10-08)

**Data.** `room_channels.kind` (`''` | `people`), migration
`2026_10_ui_people_channel` (both schema files, `migrations.lock`). The people
chat's id is derived from (tenant, room) (`pc_` + hash) and created with
`INSERT IGNORE`, so concurrent first opens converge on one row.
`ChannelStore.EnsurePeopleChannel` / `ChannelKind` (the latter reports read
errors so guards fail closed).

**Routes.** `GET /v1/rooms/{id}/people-chat` (404 until opened — the room view
reads it on entry without creating it) and `POST` (open/create, any member,
idempotent). `GET .../channels` never lists it; rename/delete → 409.

**Server guarantees (tests: `services/realtime/people_chat_test.go`,
`services/room/people_chat_test.go`, `internal/store/people_channel_test.go`).**
- chat with an agent `recipient` in the people chat → 400 (also refused for an
  agent schedule there, and for an `agent_messages` internal publish);
- `allChannels=1` streams skip people-chat events unless `includePeople=1`
  (web room view yes; the bridge's `presence=0&allChannels=1&replay=0` no);
  verdict memoized per stream, lookup failure withholds;
- `message_edited` / `message_deleted` / task puts are now scoped to the
  message's channel (they were room-wide, which would have leaked edited text);
- the bridge's `.room/` materialization pages history only for channels from
  `GET .../channels`, which excludes the people chat — the server excludes,
  the bridge needs no filter;
- `chat_message.v1` plugin hooks skip it; mention notifications still fire
  with `#/room/<id>?seq=<n>&channel=<peopleChannelId>`;
- handoff export marks it `kind: "people"` (transcript labels it), import
  restores it as the people chat and refuses agent traffic inside it;
- search hits carry `channelKind: "people"` (web label "People chat");
- unread is per channel as before; the header badge reads the people chat's id.

**Web.** Header "People chat" button (users icon, unread badge). Wide room
stage: docked right column beside the timeline, drag-resizable
(`bnbloc.peopleChatWidth`, capped at half the row), closable; narrower: a
full-screen sheet. Desktop shell: "Pop out" opens `#/people-chat/<roomId>` in
its own window (`pop_out_people_chat`, sharing one aux-window builder with the
preview pop-out; capability `people-*`). Browser: no pop-out. The panel reuses
`useRoomFeed` / `useRoomStream` (channel-scoped stream) /
`useRoomMessageActions` / `MessageFeed` / `RoomComposer` (`variant="people"`:
no agent/model pickers or segment). "Ask the agent about this" queues a quoted
reference into the main composer (`composerRefs`; from a pop-out window via a
`storage` event). Deep links / search hits / tasks into the people chat open
the panel at the message (`useMessageJump` divert).

**Mode A.** The "People only" text toggle is an "Agent | People" radio group
(icon-only on a narrow composer). People mode placeholder "Message people (the
agent won't reply)". Feed rows whose persisted payload has no agent recipient
show a users marker ("No agent reply requested"). The composer toolbar is one
row at 375px: attach + reference share a "+" menu on a narrow composer, the
model picker is icon-only there.
