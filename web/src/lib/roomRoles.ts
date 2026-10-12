// Room membership abilities, mirroring the server's one owner
// (internal/membership: Member.IsHuman, Member.MayChat; store.Role:
// CanInvite, the owner). isHuman/mayChat are pinned to one fixture
// (internal/membership/testdata/abilities.json, read by both test suites);
// canInvite reads RoomRolesThatInvite, which wiregen computes from
// store.Role.CanInvite. So the UI never offers what the server refuses.
//
// The signed-in member's membership comes from the room's member list
// (useRoomData's `me`), the one role source: the room detail carries none.

import { RoomRolesThatInvite } from "./wire.gen.ts";

export interface RoomMembership {
  // The room role (RoomRole); "" / null / undefined: not a member.
  readonly role: string | null | undefined;
  // A platform agent host's hidden membership (never a person's).
  readonly agentHost?: boolean;
}

// isHuman: a person's membership, not a platform agent host's.
export function isHuman(m: RoomMembership): boolean {
  return !!m.role && !m.agentHost;
}

// mayChat: may act in the room's conversation (post, type, drive or borrow
// an agent, create or run a schedule, add a platform agent). Visitors are
// read-only.
export function mayChat(m: RoomMembership): boolean {
  return isHuman(m) && m.role !== "visitor";
}

// canInvite: may invite, add and remove members and manage the room's
// content (pinned experts, room packs, schedules of others).
export function canInvite(m: RoomMembership): boolean {
  return isHuman(m) && (RoomRolesThatInvite as readonly string[]).includes(m.role ?? "");
}

// isOwner: may edit, transfer or delete the room itself.
export function isOwner(m: RoomMembership): boolean {
  return isHuman(m) && m.role === "owner";
}
