import { useEffect, useRef } from "react";

// useVisiblePoll runs `tick` every `intervalMs` while the page is visible, and
// once immediately when it becomes visible again, so a dashboard left in a
// background tab stops polling and is fresh the moment the member looks at it.
// `enabled=false` stops it. The latest `tick` is always used (no re-subscribe
// per render).
export function useVisiblePoll(tick: () => void, intervalMs: number, enabled = true): void {
  const tickRef = useRef(tick);
  tickRef.current = tick;
  useEffect(() => {
    if (!enabled) return;
    const run = () => {
      if (document.visibilityState === "visible") tickRef.current();
    };
    const timer = window.setInterval(run, intervalMs);
    document.addEventListener("visibilitychange", run);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", run);
    };
  }, [intervalMs, enabled]);
}
