// A channel's activity on the room view's lanes stream (channels PRD §2.4,
// wire.ChannelActivityPayload) moves its unread badge by itself: the new
// messages it reports are added once per last sequence, so a replayed or
// repeated activity adds nothing, and no client re-reads every channel's
// unread count on every message (docs/prd/2026-10-30-message-path-performance.md
// §4.4). The REST read stays the truth on opening a room.

import type { ChannelActivityPayload } from "./wire.gen.ts";

// The badge's ceiling: the server counts up to this many (store.MaxUnreadCount).
const MAX_UNREAD = 100;

/** The new messages one activity adds to its channel's badge, or null when
 *  it adds none (no messages, or a last sequence already seen). It records
 *  the sequence in seen (the caller keeps it between activities), so it is
 *  called once per activity, outside a state updater. */
export function takeChannelActivity(
  seen: Record<string, number>,
  a: Partial<ChannelActivityPayload> | undefined,
): { readonly channelId: string; readonly messages: number } | null {
  const ch = a?.channelId;
  const last = a?.lastSeq ?? 0;
  const n = a?.messages ?? 0;
  if (!ch || n <= 0 || last <= (seen[ch] ?? 0)) return null;
  seen[ch] = last;
  return { channelId: ch, messages: n };
}

/** The unread counts with a taken activity added: pure, so it can be a
 *  state updater (React may call one twice). */
export function addChannelActivity(
  unread: Readonly<Record<string, number>>,
  taken: { readonly channelId: string; readonly messages: number },
): Readonly<Record<string, number>> {
  return { ...unread, [taken.channelId]: Math.min(MAX_UNREAD, (unread[taken.channelId] ?? 0) + taken.messages) };
}
