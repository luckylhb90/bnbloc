# Rooms can be read before joining (2026-10-22)

Status: built (2026-10-22). Slice 1 (shared rooms readable by anyone in the
organization) was deployed to QA and checked there with a non-member account;
slice 2 (the owner's switch, private rooms, the room link) is built and not
yet deployed. Owner: the membership owner (`internal/membership`), the room
service (`services/room`), realtime (`services/realtime`), the gateway's room
scope (`services/gateway`, `tenant_scope.go`) and the room view
(`web/src/views/RoomView.tsx`).

Two settings of a room, independent of each other:

- **Visibility** says how a person joins: `shared` with a click, `private`
  with an invite code (four characters, `2026-10-22-short-invite-codes.md`).
- **Readable before joining** (`previewable`) says whether anyone signed in to
  the room's organization may read the room, read-only, without joining. The
  owner sets it. A shared room starts with it on (anyone could join and read
  it one click later), a private room with it off (`store.DefaultPreviewable`);
  either can be set the other way.

So a room can be shared and members-only to read, or private and open to
read ("read along, ask for the code to join"). Joining stays the only way to
act. Each room also has a link that says, where it is copied, who it opens for.

## 1. Why

- QA 2026-10-22: a colleague opened a deep link to a deliverable
  (`#/room/bbuddy-e2e?artifact=artf_…`) in a shared room they had not
  joined. The page said "This room is quiet. Send a message to chat with
  members…", "Couldn't load channels", and "This deliverable could not be
  loaded… you no longer have access", beside a "Join room" bar. Reproduced
  on QA with a non-member account (`svc:e2e-tester`): every room
  sub-resource answered 403 (`room scope not authorized`), so the feed was
  empty and the link was lost.
- The room view renders the full room around a preview. The empty states
  were written for members, so they read as the room being empty.
- The owner's questions: a shared room is joinable by anyone, so why can it
  not be previewed; then, should that be a switch the owner controls, and
  should private rooms have it too (join with an invite code); and where does
  a person get a room's address.

Checked against the code: the gateway refuses every room sub-resource to a
non-member (`handleProxy`, `roomReachable`); the room service and
realtime each check membership themselves (`Store.IsMember`,
`membership.RoomScope`); realtime already serves a reader that is not a
person (an agent host: never presence, never the people chat) through
`Member.IsHuman`.

## 2. Design

### 2.1 What a preview is

A **preview** is a signed-in caller, not a member, of a previewable room in
the caller's own organization (the session's tenant is the room's tenant).
It reads, and nothing else:

- the room card, its public channels, its member list (not the agent hosts);
- the messages of a public channel (history) and its live stream (events,
  of the kinds the feed and the deliverables use: `wire.PreviewedKind`);
- the room's deliverables: the list, the anchors that place them under
  messages, one deliverable, a version, its content and thumbnail, and an
  interactive one's live snapshot.

It never reads or does anything else: no people chat, no presence (it is
not counted present and not in the roster), no agents or hosts or
sandboxes, no tasks, jobs, runs or live views, no schedules, no attachments,
no web-app (`site`) files, and no write of any kind (chat, typing, read
cursors, channels, uploads, settings, borrowing). Joining is the one way to
act: `POST /v1/rooms/{id}/join` for a shared room, redeeming the invite code
for a private one.

A room that is not previewable shows a non-member only its card (a shared
room's, to join; any other private room is a 404, as before).

### 2.2 One owner per rule

- **The room service** owns "is this room readable by a non-member": the
  room's own `previewable` (`previewableRoom`), its probe (`?preview=1`), and
  its one read gate (`readAccess`, `requireRead`; a write keeps its member
  check, `requireMember`).
- **The membership owner** (`internal/membership`) owns "what a preview
  is". `Member` gains `Preview`; a preview is not a person in the room
  (`IsHuman` is false, so `MayChat` is false and every human-only surface
  refuses it). `Need` gains `Read`: it admits a member or a preview. Every
  other `Need` (`AnyMember`, `Human`, `Chat`) still admits members only,
  so a route reads a preview only by opting in to `Read` by name.
  `Directory.Preview` says whether a room is readable by a non-member;
  `PreviewOf` builds the `Member` (a visitor's read-only role in the room's
  tenant) in one place.
- **The gateway** owns the edge decision: a request of a non-member
  reaches a backend only if it is a GET of a route in the preview list
  (`apiPath.isPreviewRead`, one table) and the room is previewable for this
  session (the room's tenant is the session's). Everything else keeps
  `room scope not authorized`. The backends do not know the session's tenant
  (the gateway acts in the room's tenant on every path that names a room),
  so the organization boundary is the edge's.
- **Realtime** opts its history and event routes in to `Read`. The event
  stream keeps the re-check it opened under at every heartbeat: a member's
  stream ends when the membership does (a previewable room is no
  membership), a preview's when the room stops being readable. A preview's
  stream carries only the kinds marked `Preview` in `wire.RoomEventKinds`.
- **The web** owns the look: `isPreviewRoom` / `joinNeedsInviteCode`
  (`lib/apiModels.ts`), the audience wording (`lib/roomAccess.ts`) and the
  room link (`lib/hashRoute.ts` `roomLinkUrl`).

### 2.3 The room view of a preview

- The feed shows the room's messages. A preview with nothing to read says
  "No messages yet", never "quiet".
- The composer area is the join panel: a button for a shared room, the
  invite code field for a private one (`JoinPanel`). The agent banner and the
  activity rail are not shown for a preview (it cannot see runs); the
  borrow control is a member's; no read cursor is moved.
- A room that is not previewable shows its card and the join panel instead
  of the room (`RoomJoinCard`).
- A deliverable link opens its tab for a preview. Nothing is dropped from the
  URL before the room is readable.
- The member-only calls the room view makes on open (agents, shared and
  platform agents, catalogs, handoff, tasks) still answer 403 for a
  preview and the view treats them as empty, as for any failed read; not
  skipping them keeps a member's room opening in one round trip.

### 2.4 The owner's switch and the plaza

- The owner sets "Readable before joining" in the room's settings and, at
  creation, beside visibility (untouched, the visibility's default holds). The
  settings and the link's tooltip say what it means for this room
  (`roomLinkAudience`: one sentence per pair of settings).
- The plaza lists a private room whose owner opened it for reading, with its
  member count and "Open to read; joining needs an invite code", still
  locked. Any other private room stays redacted. A shared room is listed as
  before, with "Open to read before joining" when it is previewable.

### 2.5 The room link

Each room's header has a "Copy room link" button for members and previews
alike (`RoomLinkButton`): the app's own address plus `#/room/<id>`, built in
one place (`roomLinkUrl`, as a deliverable's link is). Its tooltip says who it
opens for. Opening it signs in like any room link; what the person then sees
is §2.1 (a preview), the card (§2.1), or a join.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | A preview is a signed-in member of the room's organization who has not joined. Owners and members are unchanged; an agent host stays a non-human reader. A visitor (read-only member) is unchanged. A viewer-role (read-only) organization member previews like anyone. The owner alone sets the switch. |
| Time and lifecycle | Turning the switch off (or deleting the room) ends previews within the stream heartbeat (15 s) and at the next request (probe cache TTL 10 s). Joining makes the caller a member; their open stream keeps working. A site ticket of a member who left stops at the gateway's own membership check. |
| Scale and cost | Previews add reads and one SSE stream per open tab, counted as `preview` (not present). The probe asks the room only when `?preview=1`, so every other lookup of a non-member stays one read, and is cached for the TTL. |
| Failure and recovery | No probe answer fails closed (403, as today). The room card still renders; a room that cannot be read shows its card, never "quiet". |
| Boundaries and privacy | Same organization only (the edge compares the session's tenant with the room's). A room that is not previewable shows a non-member only its card, or nothing (private). The people chat is never readable (`IsHuman` false: history and stream filter it; its id is derivable, so a route-level check protects it). The stream carries the feed and deliverable kinds only (`Preview` in the registry, a test decides every kind). Read-only: the preview list is GET-only. What a preview reads is what a joiner reads one click later, or, for a private room, what the owner chose to open. A private room made previewable is readable by the whole organization without a code: the switch says so (`roomLinkAudience`). Reads are silent: unlike joining they leave no member row (§2.6). |
| Abuse and untrusted input | The room id and channel id are the caller's: channels resolve through the room's own list; a channel that is not the room's, or is the people chat, is refused as for an agent. Scraping a previewable room's history is possible by joining (shared) or by whoever the owner opened it to; a join is visible and a preview is not. |
| One owner | `membership` owns what a preview is; the room service owns "readable by a non-member"; the gateway owns the edge list; `wire.RoomEventKinds` owns which events a preview gets; one web function says what a link opens. Architecture tests hold `Read` to the files that serve one; an e2e test holds the gateway's list and the backends' opt-ins together. |
| Defaults and fallbacks | A new shared room is previewable, a new private room is not (`DefaultPreviewable`; the web sends the choice only when the person touched it). No directory answer means no previews (fail closed). |
| External contracts | The membership probe gains an optional `preview` field, asked for with `?preview=1`; `RoomDetail`, `RoomPreview` and the plaza row gain `previewable`; create and patch accept it. The same routes answer 200 instead of 403 for a non-member of a previewable room. The Core Protocol, bridge and desktop are untouched. |
| Load on users and agents | Fewer false statements; one honest empty state; a deliverable link that opens; one place to get a room's address. No agent text, tool or rule changes: agents never see a preview (no presence, no roster). |
| Release and compatibility | Gateway, room service and realtime must be new before the web asks. Migration `2026_10_rooms_previewable` adds the column with default off and, once, turns on the rooms that were shared (they were readable in the first slice); a rerun never reopens a room an owner closed. Old room service: no column, no previews. |
| Observability | Preview streams have their own gauge (`kind="preview"`). A preview is visible in the gateway's access log as a room-scoped GET by a non-member. A dedicated log line is not built (§2.6). |
| AI capability | None: a read path. The room's AI affordances stay members' (they need `MayChat`). |
| Component or platform | Platform: it is a boundary (only the platform can decide who reads a room). |

### 2.6 Decision for the owner: reads are silent

A member's join is visible (member list, `room.member.joined`); a preview
is not. An owner who opens a room for reading chose that, which is why it is
a switch and not a rule. The gateway's access log is the only trace. If
owners want to see readers, the room could list "viewed by" counts; not built.

**Pre-mortem.**
1. *A write route admits a preview.* Writes are not on the edge's list
   (GET only), and `Read` is opt-in by name: a route that was
   `AnyMember` still refuses a preview. A gateway test walks the room's
   routes (reads off the list, every write) with a non-member of a previewable
   room and expects 403 for all but the list; an e2e test does it across the
   services; a room service test holds every write of the service to members.
2. *The people chat leaks.* A preview is non-human, so history and stream
   take the agent path (`agentMayReadChannel`, the people filter); a
   realtime test asks both for the people channel's id.
3. *Another organization reads a room.* The edge requires the session's
   tenant to be the room's; a gateway test signs in to another tenant and
   expects 403.
4. *A room closed by its owner keeps streaming.* The heartbeat re-check uses
   `Read`; a realtime test flips the room and expects the stream to end, and
   a member's stream to end when removed.
5. *A preview's stream carries agent runs.* `wire.PreviewedKind` filters it; a
   test decides every registered kind.
6. *The view still says "quiet".* The empty state reads the preview; a web
   test holds the copy for a preview and for a member.
7. *An old ticket of a member who left still opens a site.* The gateway
   checks the membership before it asks the room; a test holds it in a room
   a non-member can read.

## 4. Build order

1. Shared rooms readable (membership `Read`/`Preview`, probe, room reads,
   realtime history/events, the gateway list, the web's empty state).
   (built, deployed to QA, checked with a non-member)
2. The owner's switch: `previewable` in the store, schema and migration, the
   wire, the room service (create, patch, card, plaza, probe), the web
   (settings, create, plaza, join panel with invite code, join card); the
   room link; the review fixes (stream kinds, member re-check, site tickets,
   one preview constructor). (built, not deployed)

Not built: attachments in a preview's messages (the ticket flow checks
membership itself), a web-app ("site") deliverable's files and cover (the
gateway's own routes check membership), room sources, search over a room not
joined, "viewed by" for owners, a plaza action that opens a previewable room
without a card click.
