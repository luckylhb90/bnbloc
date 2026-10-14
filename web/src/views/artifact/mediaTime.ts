// Times of an audio or video file, for a time-range selection
// (docs/prd/2026-10-14-select-edit-review.md rule 7). Pure, unit-tested
// under node.

/** 75.4 → "1:15"; 3725 → "1:02:05". */
export function clock(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${sec}` : `${m}:${sec}`;
}

/** The selection a time range makes: a label, and the times the agent works
 *  from (seconds, one decimal). */
export function rangeSelection(start: number, end: number, duration: number): { label: string; detail: string } {
  const a = Math.min(start, end);
  const b = Math.max(start, end);
  return {
    label: `${clock(a)}–${clock(b)}`,
    detail: `From ${a.toFixed(1)} s to ${b.toFixed(1)} s (${clock(a)} to ${clock(b)}) of ${clock(duration)}.`,
  };
}
