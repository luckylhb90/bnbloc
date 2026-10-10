import { useEffect, useRef, useState } from "react";
import { CaretDownIcon, CaretUpIcon, CheckCircleIcon, CircleIcon, CompassIcon, SealCheckIcon, XIcon } from "@phosphor-icons/react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { CHECKLIST, checklistPhase, doneCount, TOUR_IN_ROOM, type OnboardingState, type TourStart } from "@/lib/onboardingTour";
import { dispatchOnboarding, useOnboarding } from "@/lib/useOnboarding";
import { Button } from "@/ui/Button";
import type { View } from "@/views/Shell";

// GettingStarted is the checklist above the account button (PRD 2026-10-10
// display names + onboarding §3): a progress bar and six items that tick
// from real events, each with "Show me" (opens a room and starts the
// matching tour step), and "Take the tour". When all six are done it
// celebrates once and folds into a dismissible "You're set" chip. On the
// collapsed sidebar rail it is a progress ring that opens the list beside it.

const CELEBRATE_MS = 2400;
const CONFETTI = Array.from({ length: 18 }, (_, i) => i);
const CONFETTI_COLORS = ["var(--color-accent)", "#f59e0b", "#60a5fa", "#f472b6", "#a78bfa", "#34d399"];

export function GettingStarted({
  subject,
  collapsed,
  activeRoom,
  onOpenRoom,
  onView,
  onNavigate,
}: {
  readonly subject: string | null;
  // The sidebar is the icon rail.
  readonly collapsed: boolean;
  readonly activeRoom: string | null;
  readonly onOpenRoom: (id: string) => void;
  readonly onView: (v: View) => void;
  // Called before a Show me navigates (closes the mobile drawer).
  readonly onNavigate?: () => void;
}) {
  const state = useOnboarding(subject);
  const phase = checklistPhase(state);
  const [popover, setPopover] = useState(false);
  const wrapRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (phase !== "celebrate") return;
    const t = window.setTimeout(() => dispatchOnboarding({ type: "celebrated" }), CELEBRATE_MS);
    return () => window.clearTimeout(t);
  }, [phase]);
  useEffect(() => {
    if (!popover) return;
    const onDown = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setPopover(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [popover]);

  if (phase === "hidden") return null;

  // Show me: start the step, then make sure a room is open for it — the
  // current one, else the member's first room, else My rooms (the welcome
  // card; the tour picks up once a room opens).
  const showMe = (start: TourStart) => {
    dispatchOnboarding({ type: "start", start });
    setPopover(false);
    onNavigate?.();
    if (activeRoom || !subject) return;
    void api
      .myRooms(subject)
      .then((r) => r.rooms?.[0]?.roomId ?? null)
      .catch(() => null)
      .then((first) => (first ? onOpenRoom(first) : onView("myrooms")));
  };

  if (phase === "chip") {
    const dismiss = () => dispatchOnboarding({ type: "dismiss" });
    // The rail has no room for a label: the seal itself dismisses.
    if (collapsed) {
      return (
        <button
          type="button"
          onClick={dismiss}
          className="set-glow tactile grid size-10 place-items-center self-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
          aria-label="You're set. Dismiss the getting-started checklist"
          title="You're set — dismiss"
        >
          <SealCheckIcon size={18} weight="fill" />
        </button>
      );
    }
    return (
      <div
        role="status"
        className="set-glow flex items-center gap-1 self-start rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] py-1 pl-2.5 pr-1 text-xs font-medium text-[var(--color-accent)]"
      >
        <SealCheckIcon size={14} weight="fill" aria-hidden />
        <span>You're set</span>
        <button
          type="button"
          onClick={dismiss}
          className="tactile grid size-6 place-items-center rounded-full hover:bg-[var(--color-surface)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
          aria-label="Dismiss the getting-started checklist"
          title="Dismiss"
        >
          <XIcon size={12} />
        </button>
      </div>
    );
  }

  const panel = (
    <ChecklistPanel
      state={state}
      celebrating={phase === "celebrate"}
      onShowMe={showMe}
      onToggle={collapsed ? () => setPopover(false) : () => dispatchOnboarding({ type: "collapse", collapsed: !state.collapsed })}
      folded={!collapsed && state.collapsed}
    />
  );
  if (!collapsed) return panel;

  const done = doneCount(state);
  const ring = 2 * Math.PI * 15;
  return (
    <div ref={wrapRef} className="relative self-center">
      <button
        type="button"
        onClick={() => setPopover((o) => !o)}
        aria-expanded={popover}
        aria-label={`Getting started, ${done} of ${CHECKLIST.length} done`}
        title={`Getting started · ${done}/${CHECKLIST.length}`}
        className="tactile relative grid size-10 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
      >
        <svg viewBox="0 0 36 36" className="absolute inset-0.5 -rotate-90" aria-hidden>
          <circle cx="18" cy="18" r="15" fill="none" strokeWidth="3" className="stroke-[var(--color-border-strong)]" />
          <circle
            cx="18"
            cy="18"
            r="15"
            fill="none"
            strokeWidth="3"
            strokeLinecap="round"
            strokeDasharray={ring}
            strokeDashoffset={ring * (1 - done / CHECKLIST.length)}
            className="stroke-[var(--color-accent)] motion-safe:transition-[stroke-dashoffset] motion-safe:duration-500"
          />
        </svg>
        <span className="tabular text-[0.65rem] font-semibold">
          {done}/{CHECKLIST.length}
        </span>
      </button>
      {popover && <div className="absolute bottom-0 left-full z-40 ml-2 w-64">{panel}</div>}
    </div>
  );
}

function ChecklistPanel({
  state,
  celebrating,
  folded,
  onShowMe,
  onToggle,
}: {
  readonly state: OnboardingState;
  readonly celebrating: boolean;
  readonly folded: boolean;
  readonly onShowMe: (start: TourStart) => void;
  readonly onToggle: () => void;
}) {
  const done = doneCount(state);
  const pct = Math.round((done / CHECKLIST.length) * 100);
  return (
    <section
      aria-label="Getting started"
      data-onboarding-checklist
      className={cn(
        "relative overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-2.5 shadow-[var(--shadow-pop)]",
        celebrating && "set-glow border-[var(--color-accent)]/50",
      )}
    >
      {celebrating && (
        <div aria-hidden className="pointer-events-none absolute inset-x-0 top-0 h-full">
          {CONFETTI.map((i) => (
            <span
              key={i}
              className="confetti-piece"
              style={
                {
                  left: `${(i * 37) % 100}%`,
                  background: CONFETTI_COLORS[i % CONFETTI_COLORS.length],
                  animationDelay: `${(i % 6) * 70}ms`,
                  "--confetti-x": `${((i * 53) % 60) - 30}px`,
                  "--confetti-r": `${(i % 2 ? 1 : -1) * (240 + ((i * 47) % 300))}deg`,
                } as React.CSSProperties
              }
            />
          ))}
        </div>
      )}
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={!folded}
        className="tactile flex w-full items-center gap-2 rounded-[var(--radius)] px-1 py-0.5 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
      >
        <span className="flex-1 text-xs font-semibold text-[var(--color-fg)]">{celebrating ? "You're set!" : "Getting started"}</span>
        <span className="tabular text-xs text-[var(--color-fg-subtle)]">
          {done}/{CHECKLIST.length}
        </span>
        {folded ? <CaretUpIcon size={12} className="text-[var(--color-fg-subtle)]" /> : <CaretDownIcon size={12} className="text-[var(--color-fg-subtle)]" />}
      </button>
      <div
        className="mx-1 mt-1.5 h-1.5 overflow-hidden rounded-full bg-[var(--color-border-strong)]"
        role="progressbar"
        aria-label="Getting started progress"
        aria-valuemin={0}
        aria-valuemax={CHECKLIST.length}
        aria-valuenow={done}
      >
        <div className="h-full rounded-full bg-[var(--color-accent)] motion-safe:transition-[width] motion-safe:duration-500" style={{ width: `${pct}%` }} />
      </div>
      {!folded && (
        <>
          <ul className="mt-2 flex flex-col">
            {CHECKLIST.map((item) => {
              const ticked = !!state.done[item.id];
              return (
                <li key={item.id} className="flex min-h-8 items-center gap-2 rounded-[var(--radius)] px-1">
                  {ticked ? (
                    <CheckCircleIcon size={16} weight="fill" className="shrink-0 text-[var(--color-accent)]" aria-label="Done" />
                  ) : (
                    <CircleIcon size={16} className="shrink-0 text-[var(--color-fg-subtle)]" aria-label="Not done yet" />
                  )}
                  <span className={cn("min-w-0 flex-1 text-xs", ticked ? "text-[var(--color-fg-subtle)]" : "text-[var(--color-fg)]")}>{item.label}</span>
                  {!ticked && !celebrating && (
                    <button
                      type="button"
                      onClick={() => onShowMe(item.showMe)}
                      className="tactile shrink-0 rounded-[var(--radius)] px-1.5 py-1 text-[0.7rem] font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent-soft)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
                      aria-label={`Show me: ${item.label}`}
                    >
                      Show me
                    </button>
                  )}
                </li>
              );
            })}
          </ul>
          {!celebrating && (
            <Button variant="ghost" size="sm" className="mt-1 w-full justify-center" onClick={() => onShowMe(TOUR_IN_ROOM)}>
              <CompassIcon size={14} />
              Take the tour
            </Button>
          )}
        </>
      )}
    </section>
  );
}
