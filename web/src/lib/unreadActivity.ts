// The shell's unread counts from the member stream (GET /v1/my/events,
// scoped to the member's rooms): the state it sends on each scope of
// rooms, then its deltas. A new message counts once (by sequence), a
// deleted one it counted no longer does, and a room the member marked read
// (on any device) is read again from the server (the rooms returned for it).

import type { UnreadDeltaPayload, UnreadReply } from "./wire.gen.ts";

export interface UnreadCounts {
  /** Per room, how many messages are unread (a room with none is absent). */
  readonly counts: Readonly<Record<string, number>>;
  /** Per room, the sequences counted since the room's count was read. */
  readonly counted: Readonly<Record<string, ReadonlySet<number>>>;
}

export const noUnread: UnreadCounts = { counts: {}, counted: {} };

/** The counts the server answered with: every room's (the stream's state),
 * or only rooms' (a reread), the others kept. */
export function unreadFromState(reply: UnreadReply, into: UnreadCounts = noUnread, rooms?: readonly string[]): UnreadCounts {
  const counts: Record<string, number> = rooms ? { ...into.counts } : {};
  const counted: Record<string, ReadonlySet<number>> = rooms ? { ...into.counted } : {};
  for (const id of rooms ?? []) {
    delete counts[id];
    delete counted[id];
  }
  for (const [id, n] of Object.entries(reply.unread ?? {})) {
    if (n > 0 && (!rooms || rooms.includes(id))) counts[id] = n;
  }
  return { counts, counted };
}

/** Applies a delta; reread are the rooms whose count is to be read again. */
export function applyUnreadDelta(u: UnreadCounts, d: UnreadDeltaPayload): { next: UnreadCounts; reread: string[] } {
  const counts: Record<string, number> = { ...u.counts };
  const counted: Record<string, ReadonlySet<number>> = { ...u.counted };
  const reread: string[] = [];
  for (const [id, rd] of Object.entries(d.rooms ?? {})) {
    if (rd.read) {
      reread.push(id);
      continue;
    }
    const seen = new Set(counted[id] ?? []);
    let n = counts[id] ?? 0;
    for (const seq of rd.seqs ?? []) {
      if (!seen.has(seq)) {
        seen.add(seq);
        n++;
      }
    }
    for (const seq of rd.deleted ?? []) {
      if (seen.delete(seq)) n = Math.max(0, n - 1);
    }
    counted[id] = seen;
    if (n > 0) counts[id] = n;
    else delete counts[id];
  }
  return { next: { counts, counted }, reread };
}
