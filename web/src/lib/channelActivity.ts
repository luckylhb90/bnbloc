// A channel's activity on the room view's lanes stream (channels PRD §2.4,
// wire.ChannelActivityPayload) moves its unread badge by itself: the new
// messages it reports are added once per last sequence, so a replayed or
// repeated activity adds nothing, and no client re-reads every channel's
// unread count on every message (docs/prd/2026-10-30-message-path-performance.md
// §4.4). The REST read stays the truth on opening a room.

import type { ChannelActivityPayload } from "./wire.gen.ts";

// The badge's ceiling: the server counts up to this many (store.MaxUnreadCount).
const MAX_UNREAD = 100;

/** The unread counts after one activity, and the last sequence seen per
 *  channel (the caller keeps it between activities). */
export function applyChannelActivity(
  unread: Readonly<Record<string, number>>,
  seen: Record<string, number>,
  a: Partial<ChannelActivityPayload> | undefined,
): Readonly<Record<string, number>> {
  const ch = a?.channelId;
  const last = a?.lastSeq ?? 0;
  const n = a?.messages ?? 0;
  if (!ch || n <= 0 || last <= (seen[ch] ?? 0)) return unread;
  seen[ch] = last;
  return { ...unread, [ch]: Math.min(MAX_UNREAD, (unread[ch] ?? 0) + n) };
}
