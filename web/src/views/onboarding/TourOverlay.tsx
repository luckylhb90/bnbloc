import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { cn } from "@/lib/cn";
import { currentStep, TOURS, type TourStep } from "@/lib/onboardingTour";
import { placeCoach, spotlightRect, type Rect, type Size } from "@/lib/tourLayout";
import { dispatchOnboarding, useOnboarding } from "@/lib/useOnboarding";
import { Button } from "@/ui/Button";
import { useModalLayer } from "@/ui/useModalLayer";

// TourOverlay is the guided first-run tour (PRD 2026-10-10 display names +
// onboarding §3): it dims the page except the current step's target (a
// `data-tour` anchor in the room) and shows a coach card with the step text,
// progress, Skip and — for steps that do not wait for a real action — Next.
// The dim layer never takes pointer events, so the member always does the
// real thing (types, sends, opens a file) on the real control. It shows only
// inside a room, and steps aside while a dialog is open.

interface Geometry {
  readonly spot: Rect | null;
  // The step's primary anchor is not on screen (the card shows the fallback).
  readonly primaryMissing: boolean;
  readonly blocked: boolean;
  readonly view: Size;
  readonly card: Size;
}

const DEFAULT_CARD: Size = { width: 340, height: 190 };
const MODAL_OPEN = '[aria-modal="true"]:not([data-tour-card])';

// visibleRect is the part of `el` its scrolling ancestors actually show (a
// card half scrolled under the feed's top edge is cut there), or null when
// it is not rendered.
function visibleRect(el: Element): { rect: Rect; clipped: boolean } | null {
  const r = el.getBoundingClientRect();
  if (r.width <= 0 || r.height <= 0) return null;
  let top = r.top;
  let left = r.left;
  let bottom = r.bottom;
  let right = r.right;
  for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
    const o = getComputedStyle(p);
    if (!/(auto|scroll|hidden|clip)/.test(o.overflowY + o.overflowX)) continue;
    const c = p.getBoundingClientRect();
    top = Math.max(top, c.top);
    left = Math.max(left, c.left);
    bottom = Math.min(bottom, c.bottom);
    right = Math.min(right, c.right);
  }
  const clipped = top > r.top || bottom < r.bottom;
  if (bottom - top <= 0 || right - left <= 0) return { rect: { top: r.top, left: r.left, width: 0, height: 0 }, clipped: true };
  return { rect: { top, left, width: right - left, height: bottom - top }, clipped };
}

// findTarget: the first anchor id in the step's priority list that is
// rendered; within one id the last match (the newest deliverable card).
function findTarget(targets: readonly string[]): { el: Element; rect: Rect; clipped: boolean; index: number } | null {
  for (let index = 0; index < targets.length; index++) {
    const all = Array.from(document.querySelectorAll(`[data-tour="${targets[index]}"]`));
    for (let i = all.length - 1; i >= 0; i--) {
      const v = visibleRect(all[i]);
      if (v) return { el: all[i], ...v, index };
    }
  }
  return null;
}

// blockedBy: a dialog is open that does not hold the target (the share
// dialog, members, settings) — the tour steps aside. A layer that holds it
// (the phone's preview overlay, the rail drawer) does not block.
function blocked(target: Element | null): boolean {
  return Array.from(document.querySelectorAll(MODAL_OPEN)).some((m) => !target || !m.contains(target));
}

const reducedMotion = () => typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

export function TourOverlay({ subject, inRoom }: { readonly subject: string | null; readonly inRoom: boolean }) {
  const onboarding = useOnboarding(subject);
  // Room steps need a room; the start step lives on the home view.
  const active = currentStep(onboarding.tour);
  const step = active && (inRoom ? active.id !== "start" : active.id === "start") ? active : null;
  const geo = useTourGeometry(step);
  if (!step || !geo || geo.blocked) return null;
  return createPortal(<TourLayer step={step} stepIndex={onboarding.tour.step} total={TOURS[onboarding.tour.tour].length} geo={geo} />, document.body);
}

// useTourGeometry follows the target and the card every frame while a step
// is shown: resize, scroll, the docked preview opening, a card arriving in
// the feed — the cutout tracks all of it. State changes only when the
// geometry does.
function useTourGeometry(step: TourStep | null): Geometry | null {
  const [geo, setGeo] = useState<Geometry | null>(null);
  const scrolledFor = useRef<string | null>(null);
  useEffect(() => {
    if (!step) {
      setGeo(null);
      return;
    }
    let raf = 0;
    let last = "";
    const tick = () => {
      const view = { width: window.innerWidth, height: window.innerHeight };
      const found = findTarget(step.targets);
      if (found && scrolledFor.current !== step.id) {
        scrolledFor.current = step.id;
        if (found.clipped) found.el.scrollIntoView({ block: "nearest", behavior: reducedMotion() ? "auto" : "smooth" });
      }
      const cardEl = document.querySelector("[data-tour-card]");
      const card = cardEl ? { width: cardEl.clientWidth, height: cardEl.clientHeight } : DEFAULT_CARD;
      const next: Geometry = {
        spot: found && found.rect.height > 0 ? spotlightRect(found.rect, view) : null,
        primaryMissing: !found || found.index > 0,
        blocked: blocked(found?.el ?? null),
        view,
        card,
      };
      const key = JSON.stringify(next);
      if (key !== last) {
        last = key;
        setGeo(next);
      }
      raf = requestAnimationFrame(tick);
    };
    tick();
    return () => cancelAnimationFrame(raf);
  }, [step]);
  return geo;
}

function TourLayer({ step, stepIndex, total, geo }: { readonly step: TourStep; readonly stepIndex: number; readonly total: number; readonly geo: Geometry }) {
  const cardRef = useRef<HTMLDivElement | null>(null);
  const titleId = useId();
  // Steps that only Next advances hold focus in the card (Tab cycles in it);
  // steps that wait for a real action leave the page usable from the keyboard.
  const trap = step.advance === "next";
  useModalLayer(cardRef, { open: trap, lockScroll: false });

  // Esc skips the tour — unless something else (a menu, a dialog, the
  // composer's mention list) already handled the key.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || document.querySelector(MODAL_OPEN)) return;
      dispatchOnboarding({ type: "skip" });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // The Ask step puts the cursor in the composer (not on a phone, where it
  // would pop the keyboard over the coach card).
  useEffect(() => {
    if (step.id !== "ask" || window.innerWidth < 640) return;
    document.querySelector<HTMLElement>('[data-tour="composer"] textarea')?.focus();
  }, [step.id]);

  const place = placeCoach(geo.spot, geo.card, geo.view);
  const text = geo.primaryMissing && step.fallback ? step.fallback : step.text;
  const sheet = place.mode === "sheet";
  return (
    <>
      {geo.spot ? (
        <div
          aria-hidden
          data-tour-spotlight
          className="pointer-events-none fixed z-[45] rounded-[var(--radius-lg)] ring-2 ring-[var(--color-accent)] shadow-[0_0_0_9999px_rgba(0,0,0,0.55)] motion-safe:transition-[top,left,width,height] motion-safe:duration-300 motion-safe:ease-out"
          style={{ top: geo.spot.top, left: geo.spot.left, width: geo.spot.width, height: geo.spot.height }}
        />
      ) : (
        <div aria-hidden data-tour-spotlight className="pointer-events-none fixed inset-0 z-[45] bg-black/40" />
      )}
      <div
        ref={cardRef}
        data-tour-card
        role="dialog"
        aria-modal={trap || undefined}
        aria-labelledby={titleId}
        tabIndex={-1}
        className={cn(
          "fixed z-[46] flex flex-col gap-3 border border-[var(--color-border-strong)] bg-[var(--color-elevated)] p-4 text-[var(--color-fg)] shadow-[var(--shadow-pop)] focus:outline-none tour-enter",
          sheet
            ? cn("inset-x-0 w-full", place.edge === "bottom" ? "bottom-0 rounded-t-[var(--radius-xl)] pb-[calc(1rem+env(safe-area-inset-bottom))]" : "top-0 rounded-b-[var(--radius-xl)] pt-[calc(1rem+env(safe-area-inset-top))]")
            : "w-[min(21rem,calc(100vw-1rem))] rounded-[var(--radius-lg)] motion-safe:transition-[top,left] motion-safe:duration-300",
        )}
        style={place.mode === "float" ? { top: place.top, left: place.left } : undefined}
      >
        {total > 1 && (
          <div className="flex items-center gap-2">
            <span className="tabular text-xs font-medium text-[var(--color-fg-subtle)]">
              Step {stepIndex + 1} of {total}
            </span>
            <div className="flex flex-1 gap-1" aria-hidden>
              {Array.from({ length: total }, (_, i) => (
                <span
                  key={i}
                  className={cn("h-1 flex-1 rounded-full", i <= stepIndex ? "bg-[var(--color-accent)]" : "bg-[var(--color-border-strong)]")}
                />
              ))}
            </div>
          </div>
        )}
        <div aria-live="polite">
          <h2 id={titleId} className="text-sm font-semibold tracking-tight">
            {step.title}
          </h2>
          <p className="mt-1 text-sm leading-relaxed text-[var(--color-fg-muted)]">{text}</p>
          {step.waiting && (
            <p className="mt-2 flex items-center gap-1.5 text-xs text-[var(--color-fg-subtle)]">
              <span className="size-1.5 rounded-full bg-[var(--color-accent)] tour-pulse" aria-hidden />
              {step.waiting}
            </p>
          )}
        </div>
        <div className="flex items-center justify-between gap-2">
          <Button variant="ghost" size="sm" onClick={() => dispatchOnboarding({ type: "skip" })} title="Skip the tour (Esc)">
            Skip tour
          </Button>
          {step.button && (
            <Button variant="primary" size="sm" autoFocus={trap} onClick={() => dispatchOnboarding({ type: "next" })}>
              {step.button}
            </Button>
          )}
        </div>
      </div>
    </>
  );
}
