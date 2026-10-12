import { sameUser } from "./format.ts";

// A room is "gone" for this member when the gateway answers its detail with
// 404 (deleted, or a private room they no longer see) or 403 (not a member),
// or when the room stream announces the end of access (room_deleted, or this
// member's member_removed). Every client surface that remembers room ids drops
// a gone room the moment one surface learns it, so nothing keeps requesting it
// (the stale-room 404 loop, docs/bugs/2026-10-08-stale-room-404-loop.md).

export type RoomAccessEnd = "deleted" | "removed";

// roomGoneText is what every surface says when a room is gone for the
// member ("missing": the detail answered 404/403).
export function roomGoneText(reason: RoomAccessEnd | "missing"): string {
  if (reason === "deleted") return "This room was deleted.";
  if (reason === "removed") return "You were removed from this room.";
  return "This room no longer exists or you no longer have access.";
}

// isRoomGoneStatus: the room-detail statuses that mean "this room is not
// reachable for you any more" (as opposed to a transient failure).
export function isRoomGoneStatus(status: number): boolean {
  return status === 404 || status === 403;
}

// roomAccessEnded maps a room stream event to the access it ended, or null.
// The server closes the stream right after these events, so the subscriber
// must stop instead of reconnecting into 403s.
export function roomAccessEnded(
  e: { readonly kind?: string; readonly removedUser?: string },
  subject: string | null,
): RoomAccessEnd | null {
  if (e.kind === "room_deleted") return "deleted";
  if (e.kind === "member_removed" && sameUser(e.removedUser, subject)) return "removed";
  return null;
}

// withoutRoom returns rooms minus roomId, or the same array when it is absent
// (so a React state setter bails out instead of re-rendering / re-polling).
export function withoutRoom<T extends { readonly roomId: string }>(rooms: T[], roomId: string): T[] {
  return rooms.some((r) => r.roomId === roomId) ? rooms.filter((r) => r.roomId !== roomId) : rooms;
}

// Session-wide notification: the surface that learns a room is gone reports
// it; every surface that holds room ids (the sidebar list and its unread poll)
// prunes it. A freshly fetched server list stays authoritative — a member who
// is re-invited gets the room back on the next listing.
const listeners = new Set<(roomId: string) => void>();

export function reportRoomGone(roomId: string): void {
  for (const listener of [...listeners]) listener(roomId);
}

export function onRoomGone(listener: (roomId: string) => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
