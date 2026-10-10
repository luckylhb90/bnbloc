import { useEffect, useRef, useState, type RefObject } from "react";

// Capability-driven adaptation, not device detection.
//
// Asking "is this a phone?" produces a combinatorial mess: an iPad is touch +
// wide, a shrunken desktop window is mouse + narrow. Those two axes are
// independent, so we measure them independently:
//
//   useCoarsePointer() -> how the user points (touch vs mouse)
//   useStage(ref)      -> how much room a component actually has
//
// Screen width is deliberately never consulted. A component that reacts to its
// own container works identically in a phone viewport, a 720px Tauri window,
// and a desktop column narrowed by an open dock — one rule instead of one rule
// per device.

export type Stage = "compact" | "medium" | "wide";

// Chat needs ~28rem before a side column is worth its space, and ~58rem before
// a third column is. Expressed in rem so the stage follows the user's font
// size: someone at 200% zoom gets the compact layout they actually need.
const MEDIUM_MIN_REM = 34;
const WIDE_MIN_REM = 58;

function rootFontPx(): number {
  const parsed = Number.parseFloat(getComputedStyle(document.documentElement).fontSize);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 16;
}

export function stageForWidth(px: number, remPx = 16): Stage {
  if (px >= WIDE_MIN_REM * remPx) return "wide";
  if (px >= MEDIUM_MIN_REM * remPx) return "medium";
  return "compact";
}

// Observes an element and reports which layout stage it can afford.
export function useStage(ref: RefObject<HTMLElement | null>): Stage {
  const [stage, setStage] = useState<Stage>("wide");
  // Held in a ref so the observer callback never needs re-subscribing.
  const current = useRef<Stage>("wide");

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const remPx = rootFontPx();
    const apply = (width: number) => {
      const next = stageForWidth(width, remPx);
      if (next === current.current) return;
      current.current = next;
      setStage(next);
    };
    apply(el.getBoundingClientRect().width);
    const ro = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (entry) apply(entry.contentRect.width);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);

  return stage;
}

function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() =>
    typeof window === "undefined" ? false : window.matchMedia(query).matches,
  );
  useEffect(() => {
    const mq = window.matchMedia(query);
    const onChange = (e: MediaQueryListEvent) => setMatches(e.matches);
    setMatches(mq.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [query]);
  return matches;
}

// True when the primary pointer is imprecise (finger/stylus). Drives hit-target
// sizing and whether hover-only affordances need an always-visible fallback.
export function useCoarsePointer(): boolean {
  return useMediaQuery("(pointer: coarse)");
}
