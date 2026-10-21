# A deleted room's agent workspaces were never removed (2026-10-22)

Seen on a member's computer (here `maya`): after the long-task smoke and a
probe room were deleted, their sandbox workdirs (`~/bnbloc-workspaces/<space>`)
and, since durable homes (docs/prd/2026-10-21-durable-sandbox-homes.md), the
spaces' homes stayed. Waiting changed nothing. About 90 workdirs of deleted
rooms had piled up.

## Root cause

The bridge removed a room's workspaces only in `dropGoneRoom`, and only when
the room's event stream answered **404**. That never happens for a deleted
room:

- The owner's session token is room-scoped. A deleted room is no longer in
  its scope, so the stream answers **403** ("room scope not authorized"),
  as for a room that never existed. Checked live on QA: a deleted room, a
  live room and a made-up id answer 403, 200 and 403. On 403 the bridge
  forgets the bindings (`workspaces.forgetRoom`) and keeps the files, which
  orphans them for good.
- `reconcile` usually saw the room vanish from the listing first. It
  cancelled the stream, and with it any chance of that answer.
- The room detail (`GET /v1/rooms/{id}`) answers 404 for a deleted room,
  but it is tenant-scoped: it answers 404 for a live room of another
  tenant too (`TestBridgeKeepsRoomWhoseDetailIsOutOfTenant`), so it cannot
  decide either.
- Rooms deleted while the desktop was off were never looked at again.

Every signal the bridge had was absence (not listed, 403, 404), and
absence cannot tell "deleted" from "not visible right now". Deleting on
absence would destroy a live room's files on a misread. Never deleting
piles them up.

## Fix: delete only on positive evidence, in two steps

- **Positive evidence.** The room service answers which of the given rooms
  are deleted (`GET /v1/my/deleted-rooms?ids=…`). It reads the store's
  soft-delete marker (`RoomStore.DeletedRooms`, across tenants: room ids
  are global), never absence. An id that is live, unknown or someone
  else's answers nothing. The room stream's `room_deleted` event, sent
  when the room is deleted while the bridge watches it, is evidence too.
- **Two steps.** Evidence marks the room's workspaces gone (a tombstone in
  `workspaces.json`): nothing is deleted yet, and the bindings stay. A sweep
  (at start, then every 10 minutes) asks about the rooms it holds
  workspaces for and does not watch. A tombstone at least a day old whose
  room is still reported deleted is purged: the core's workspace, the
  sandbox space with its home (purge) and the workdir under the workspaces
  root. A slot that fails to purge (the sandbox not up) stays for the next
  sweep. A room no longer reported deleted loses its tombstone.
- **Never on absence.** A 403, a 404, a missing listing or a failed check
  deletes nothing. `dropGoneRoom` stops watching and forgets the room's
  sessions, but keeps its bindings, so a room that comes back resumes its
  workspace.

## Why it was not caught

- Lens 8 (defaults, unknowns): "is 'not visible' treated as 'does not
  exist'?". The cleanup was written for the 404 the test fake answered, not
  the 403 the real gateway answers. The fake answered what the code
  expected (LESSONS: "Make a test fake refuse what the real side
  refuses").
- Lens 2 (time and lifecycle): nothing asked what happens to a room deleted
  while its member's desktop is off.

Tests: `TestDeletedRoomWorkspacesArePurgedOnEvidence` (a failed question
marks nothing; a day-old mark is purged: core workspace, purged space,
workdir; a room the owner left keeps all), `TestGoneRoomKeepsItsWorkspaces`
(404 and 403 keep the bindings), `TestRoomDeletedEventMarksTheRoom`,
`TestDeletedRoomMarkClearsWhenTheRoomIsSeen`; the store contract's
`DeletedRooms` on memory and MySQL; `TestDeletedRoomsAnswersOnlyDeletions`
(room service) and `TestMyDeletedRoomsRoute` (gateway). The event and the
day's wait each fail with their part reverted.

Release order: the services (the new route) before the desktop that asks
it. A bridge facing an older room service gets a 404 for the question and
removes nothing.

Workdirs left by deleted rooms before this change have no binding any
more (the old 403 path forgot them), so the sweep never sees them. They
stay until someone removes them by hand.

Landed in: the commit that adds this record.
