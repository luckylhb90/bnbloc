# Clients keep requesting rooms that were deleted or are no longer visible

Date: 2026-10-08
Status: fixed (uncommitted at time of writing)
PRD item: H6 in `docs/prd/2026-10-08-platform-hardening-and-team-features.md`

## Reproduction

1. Member A opens room `r` (browser tab or desktop window, URL `#/room/r`),
   or pops an artifact out into a preview window (`#/preview/r/<artifact>`).
2. The owner deletes `r` (or removes A). Alternatively A reloads a bookmarked /
   session-restored `#/room/r` after the deletion.
3. Gateway logs show repeated `GET /v1/rooms/r` 404s and
   `GET /v1/rooms/r/events` 403s for that member and their bridge.

## Root cause

No client surface treated "the room is gone" as a terminal answer:

1. **Room view** (`web/src/views/room/useRoomData.ts`): the room detail fetch
   swallowed every error. A 404 left the view on a room that never loads while
   its stream kept reconnecting (EventSource cannot see the 403, so
   `subscribeRoom` backs off and re-auths forever). The `#/room/r` route stayed
   in the URL, so every reload / session restore started the loop again.
2. **Sidebar quick access + unread poll** (`web/src/views/Shell.tsx`): the room
   list is refetched only on view/room change, so a room that disappeared kept
   its id in the 25 s unread poll until the next navigation.
3. **Preview window** (`web/src/views/StandalonePreview.tsx`): it ignored
   `room_deleted` / `member_removed`, so after the server closed its stream it
   reconnected into 403s for as long as the window stayed open.
4. **Bridge** (`services/bridge/bridge.go`): the per-room sync loop polls
   `GET /v1/rooms/{id}` (~every 32 s) and the stream loop re-subscribes with
   backoff; neither treated 404/403 as final. Discovered rooms were dropped only
   at the next reconcile, static `BNBLOC_BRIDGE_ROOMS` entries never. A failed
   room listing was also read as "no rooms", tearing every stream down and
   re-subscribing all of them on the next tick.

The desktop shell (Rust) keeps no room ids and fetches no rooms; nothing to
change there.

## Fix

- `web/src/lib/roomGone.ts` (one rule, node-tested in `roomGone.test.ts`):
  `isRoomGoneStatus` (404/403), `roomAccessEnded` (room_deleted / own
  member_removed — previously inlined in `useRoomStream`), `withoutRoom`, and a
  session-wide `reportRoomGone` / `onRoomGone` notification.
- Room view: a 404/403 room detail or a revoking stream event calls one
  `leaveGoneRoom` (once): reports the room gone, shows one toast, and navigates
  away so the route no longer points at it (unmount also stops the stream).
- Shell: subscribes to `onRoomGone` and prunes the quick-access list, which
  drops the id from the unread poll. A fresh server listing stays
  authoritative (a re-invited member gets the room back).
- Preview window: stops its stream on revocation and shows a "room is gone"
  state; an artifact 404/403 is confirmed against the room detail before the
  window declares the room gone.
- Bridge: `consumeRoom` returns a structured `gatewayError`; a 404/403 on the
  stream or the sync room lookup calls `dropGoneRoom` (cancel stream + sync,
  forget per-room state) and puts the room in a one-minute cooldown that
  reconcile honours (also for the token-invalidation pass). A failed discovery
  keeps the current set. Tests: `services/bridge/room_gone_test.go`.
