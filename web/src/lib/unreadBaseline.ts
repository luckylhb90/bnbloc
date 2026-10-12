// unreadBaseline picks the sequence of the first message a member has not
// read, given the channel's loaded rows and the server's unread count for it
// (captured before this visit marked anything read). The "New" divider is
// drawn above that message. No unread → no divider (null). When the unread
// run reaches beyond the loaded page, the divider sits above the oldest
// loaded message. The server counts room sequences, and the parts of one
// agent batch share one, so sequences are counted once each.
export function unreadBaseline(feed: readonly { readonly seq?: number }[], unread: number): number | null {
  if (unread <= 0) return null;
  const seqs = [...new Set(feed.map((m) => m.seq ?? 0).filter((s) => s > 0))].sort((a, b) => a - b);
  if (seqs.length === 0) return null;
  return seqs.length >= unread ? seqs[seqs.length - unread] : seqs[0];
}
