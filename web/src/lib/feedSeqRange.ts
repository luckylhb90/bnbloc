// feedSeqRange is the room-sequence range of the loaded feed rows, or null
// when no row has one yet.
export function feedSeqRange(feed: readonly { readonly seq?: number }[]): { min: number; max: number } | null {
  let min = 0;
  let max = 0;
  for (const m of feed) {
    const seq = m.seq ?? 0;
    if (seq <= 0) continue;
    if (min === 0 || seq < min) min = seq;
    if (seq > max) max = seq;
  }
  return min > 0 ? { min, max } : null;
}
