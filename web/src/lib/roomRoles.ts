// Room membership abilities, mirroring the server's one owner
// (internal/membership: Member.IsHuman, Member.MayChat). The two are pinned
// to one fixture (internal/membership/testdata/abilities.json, read by both
// test suites), so the UI never offers what the server refuses.

export interface RoomMembership {
  // The room role: owner | admin | member | visitor ("" / undefined: none).
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
