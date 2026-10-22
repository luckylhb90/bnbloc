# Shared rooms can be read before joining (2026-10-22)

Status: built (2026-10-22), not yet deployed or checked on QA. Owner: the membership owner
(`internal/membership`), the room service (`services/room`), realtime
(`services/realtime`), the gateway's room scope (`services/gateway`,
`tenant_scope.go`) and the room view (`web/src/views/RoomView.tsx`).

A room's visibility is `shared` (join without an invite) or `private`
(invite). Anyone in the organization can already join a shared room and
then read all of it, so reading it first costs nothing and hides nothing.
Today a non-member gets only a minimal card, and the room view around it
says things that are false for them. This makes a shared room readable,
read-only, by anyone signed in to the room's organization; joining stays
the only way to act.

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
- The owner's question: a shared room is joinable by anyone, so why can it
  not be previewed?

Checked against the code: the gateway refuses every room sub-resource to a
non-member (`handleProxy`, `roomReachable`); the room service and
realtime each check membership themselves (`Store.IsMember`,
`membership.RoomScope`); realtime already serves a reader that is not a
person (an agent host: never presence, never the people chat) through
`Member.IsHuman`.

## 2. Design

### 2.1 What a preview is

A **preview** is a signed-in caller, not a member, of a `shared` room in
the caller's own organization (the session's tenant is the room's tenant).
It reads, and nothing else:

- the room card (already served), its public channels, its member list;
- the messages of a public channel (history) and its live stream (events);
- the room's deliverables: the list, one deliverable, a version, its
  content and thumbnail.

It never reads or does anything else: no people chat, no presence (it is
not counted present and not in the roster), no agents or hosts or
sandboxes, no tasks, jobs or live views, no schedules, no attachments in
this slice, no app state, and no write of any kind (chat, typing, read
cursors, channels, uploads, settings, borrowing). Joining
(`POST /v1/rooms/{id}/join`, already exempt) is the one way to act.

### 2.2 One owner per rule

- **The membership owner** (`internal/membership`) owns "what a preview
  is". `Member` gains `Preview`; a preview is not a person in the room
  (`IsHuman` is false, so `MayChat` is false and every human-only surface
  refuses it). `Need` gains `Read`: it admits a member or a preview. Every
  other `Need` (`AnyMember`, `Human`, `Chat`) still admits members only,
  so a route reads a preview only by opting in to `Read` by name. A
  directory that can tell a preview from a non-member implements the
  optional `Previewer` (`Preview(roomID) (Member, bool)`); the room
  client answers it from the room service's membership probe, which now
  also says `preview: true` for a non-member of a live `shared` room.
- **The room service** owns the answer to "is this room readable by a
  non-member": the probe, and `mayRead` (a member, or the room is shared)
  used by exactly the read routes of §2.1 in place of each handler's own
  `IsMember`.
- **The gateway** owns the edge decision: a request of a non-member
  reaches a backend only if it is a GET of a route in the preview list
  (`previewRoute`, one table) and the room is previewable for this session
  (shared, and the room's tenant is the session's tenant). Everything
  else keeps `room scope not authorized`. The backends do not know the
  session's tenant (the gateway acts in the room's tenant on every path
  that names a room), so the organization boundary is the edge's.
- **Realtime** opts its history and event routes in to `Read`. The event
  stream's heartbeat re-checks with `Read` too, so a room made private
  ends its previews within a heartbeat.
- **The web** owns the look: `lib/roomRoles.ts` and the room view treat
  `room.preview` as "reading", not "empty".

### 2.3 The room view of a preview

- The feed shows the room's messages. The empty state of a preview never
  says "quiet": a room with no messages says "No messages yet. Join to
  start."
- The composer area is the join bar (exists). The agent banner is not
  shown for a preview, and the channels now load (no retry error). The
  member-only calls the room view makes on open (agents, shared and
  platform agents, catalogs, handoff, tasks) still answer 403 for a
  preview and the view treats them as empty, as for any failed read; not
  skipping them keeps a member's room opening in one round trip.
- A deliverable link opens its tab for a preview (the preview reads it).
  Nothing is dropped from the URL before the room is readable.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | A preview is a signed-in member of the room's organization who has not joined. Owners and members are unchanged; an agent host stays a non-human reader. A visitor (read-only member) is unchanged. A viewer-role (read-only) organization member previews like anyone. |
| Time and lifecycle | A room made private (or deleted) ends previews within the stream heartbeat (15 s, `Read` re-check) and at the next request (probe cache TTL 10 s). Joining makes the caller a member; their open stream keeps working (a member passes `Read`). No stored state. |
| Scale and cost | Previews add reads and one SSE stream per open tab, not counted present. The probe is cached per (room, user) for the TTL. The gateway asks the probe only on a non-member's request. History and events are paged and bounded as for members. |
| Failure and recovery | No probe answer fails closed (403, as today). The room card still renders; the view says the room could not be read and offers a retry, never "quiet". |
| Boundaries and privacy | Same organization only (the edge compares the session's tenant with the room's). Private rooms are untouched (probe says nothing, 404 card as today). The people chat is never readable (`IsHuman` false: history and stream filter it; its id is derivable, so a route-level check, not hiding the id, protects it). Read-only: the preview list is GET-only. What a preview reads is what a joiner reads one click later. Reads are silent: unlike joining, they leave no member row. §2.4. |
| Abuse and untrusted input | The room id and channel id are the caller's: channels resolve through the room's own list; a channel that is not the room's, or is the people chat, is refused as for an agent. Scraping a shared room's history is possible today by joining; a join is audited and a preview is not (§2.4). |
| One owner | `membership` owns what a preview is; the room service owns "readable by a non-member"; the gateway owns the edge list; architecture tests hold that only the listed routes use `Read` and that the gateway list and the backends agree (§4). |
| Defaults and fallbacks | A new room is `private`: nothing becomes readable until an owner shares it. No `Previewer` in a directory (dev static sets) means no previews (fail closed). |
| External contracts | The membership probe gains an optional `preview` field (additive). No wire change for clients: the same routes answer 200 instead of 403. The Core Protocol, bridge and desktop are untouched: a bridge's stream is a member's. |
| Load on users and agents | Fewer false statements; one honest empty state; a deliverable link that opens. No agent text, tool or rule changes: agents never see a preview (no presence, no roster). |
| Release and compatibility | Gateway and room service must both be new before the web asks; an old gateway answers 403 and the new view shows the join card with its honest empty state. Pre-launch: no flag. |
| Observability | The room service's probe answers `preview:true` and the gateway's refusals stay 403 with the same message; a preview is visible in the gateway's access log as a room-scoped GET by a non-member. A dedicated log line (`bnbloc.gateway.room_preview`) is not built (§2.4). |
| AI capability | None: a read path. The room's AI affordances stay members' (they need `MayChat`). |
| Component or platform | Platform: it is a boundary (only the platform can decide who reads a room). |

### 2.4 Decision for the owner: reads are silent

A member's join is visible (member list, `room.member.joined`); a preview
is not. A room's owner who shared it for discovery may not expect
silent readers. The gateway's access log (a room-scoped GET by a
non-member) is the only trace in this slice. If owners want to see readers, the room card could list "viewed by"
counts, or an owner switch "readable before joining" (default on) could be
added; neither is built, and shared rooms are readable by anyone in the
organization today after one click.

**Pre-mortem.**
1. *A write route admits a preview.* Writes are not on the edge's list
   (GET only), and `Read` is opt-in by name: a route that was
   `AnyMember` still refuses a preview. A gateway test walks the room's
   routes (reads off the list, every write) with a non-member of a shared
   room and expects 403 for all but the list; a membership test holds
   `AnyMember`/`Human`/`Chat`; a room service test holds every write of
   the service to members.
2. *The people chat leaks.* A preview is non-human, so history and stream
   take the agent path (`agentMayReadChannel`, the people filter); a
   realtime test asks both for the people channel's id.
3. *Another organization reads a shared room.* The edge requires the
   session's tenant to be the room's; a gateway test signs in to another
   tenant and expects 403.
4. *A room turned private keeps streaming.* The heartbeat re-check uses
   `Read`; a realtime test flips the room and expects the stream to end.
5. *The view still says "quiet".* The empty state reads `room.preview`; a
   web test holds the copy for a preview and for a member.

## 4. Build order

1. `membership`: `Preview`, `Need.Read`, `Previewer`; the room client and
   probe (`preview`); realtime history/events/heartbeat on `Read`.
2. The room service's `mayRead` on channels, members and artifact reads;
   the gateway's preview list and tenant check; tests of §3's
   pre-mortem.
3. The web: preview state, honest empty state, no member-only calls, the
   deliverable link; QA with a non-member account.

Not built: attachments in a preview's messages (the ticket flow checks
membership itself), a web-app ("site") deliverable's files and cover (the
gateway's own routes check membership), room sources, search over a room
not joined, an owner switch for "readable before joining".
