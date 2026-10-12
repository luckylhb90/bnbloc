import { useEffect, useRef } from "react";

// The one way the client polls the server (web M9; boundary rule 5): while
// the page is visible, every intervalMs, and once at once when it becomes
// visible again, so a view left in a background tab stops polling and is
// fresh the moment the member looks at it.

// visiblePoll starts polling tick outside React; it returns the stop.
export function visiblePoll(tick: () => void, intervalMs: number): () => void {
  const run = () => {
    if (document.visibilityState === "visible") tick();
  };
  const timer = window.setInterval(run, intervalMs);
  document.addEventListener("visibilitychange", run);
  return () => {
    window.clearInterval(timer);
    document.removeEventListener("visibilitychange", run);
  };
}

// useVisiblePoll is visiblePoll for a component. `enabled=false` stops it.
// The latest `tick` is always used (no re-subscribe per render).
export function useVisiblePoll(tick: () => void, intervalMs: number, enabled = true): void {
  const tickRef = useRef(tick);
  tickRef.current = tick;
  useEffect(() => {
    if (!enabled) return;
    return visiblePoll(() => tickRef.current(), intervalMs);
  }, [intervalMs, enabled]);
}
