import { useEffect, useRef, useState } from "react";
import { useCoarsePointer } from "@/lib/capability";
import { cn } from "@/lib/cn";
import directionsSheet from "@/assets/mascot/human-directions.png";
import reactionsSheet from "@/assets/mascot/human-reactions.png";

// A self-contained sprite mascot: two 3x3 sheets (head directions + reactions)
// stepped via background-position on a 300% background. It watches the pointer
// to turn its head toward the cursor and plays a brief reaction when booped.
// In floating mode it becomes a fixed, draggable companion that idle-bobs.
// No external dependency and no wasm — just two transparent PNGs and this file.

// Row-major cell order in each 3x3 sheet.
const DIRECTIONS = [
  "up-left", "up", "up-right",
  "left", "center", "right",
  "down-left", "down", "down-right",
] as const;
type Direction = (typeof DIRECTIONS)[number];

const REACTIONS = [
  "blink", "heart", "sparkle",
  "surprised", "delighted", "bashful",
  "sleepy", "dizzy", "laughing",
] as const;
type Reaction = (typeof REACTIONS)[number];

// Reactions cycled through on repeated boops before the dizzy payoff.
const PAYOFFS: Reaction[] = ["heart", "sparkle", "delighted", "laughing", "surprised", "bashful"];
const BOOP_PAYOFF_MS = 90;
const BOOP_END_MS = 620;
const DIZZY_WINDOW_MS = 900;
const DIZZY_AFTER = 5;
const DIZZY_END_MS = 1100;
// Pointer nearer than this (px) leaves the head centered instead of jittering.
const AIM_DEADZONE_PX = 26;
// Pointer travel (px) that turns a press into a drag rather than a boop.
const DRAG_THRESHOLD_PX = 4;
// Gap kept from the viewport edges when placing/clamping the floating mascot.
const FLOAT_MARGIN_PX = 24;

// Maps a pointer angle (radians, 0 = right, CCW) to one of the 8 outer cells.
function directionForAngle(angle: number): Direction {
  const deg = ((angle * 180) / Math.PI + 360) % 360;
  const octant = Math.round(deg / 45) % 8;
  return (["right", "up-right", "up", "up-left", "left", "down-left", "down", "down-right"] as const)[octant];
}

// background-position for a grid index on a 300% sheet: 0% / 50% / 100% steps.
function cell(index: number) {
  const col = index % 3;
  const row = Math.floor(index / 3);
  return { backgroundPosition: `${col * 50}% ${row * 50}%` };
}

function clamp(value: number, min: number, max: number) {
  return Math.min(Math.max(value, min), max);
}

const layer: React.CSSProperties = {
  position: "absolute",
  inset: 0,
  backgroundSize: "300% 300%",
  backgroundRepeat: "no-repeat",
  transition: "opacity 120ms ease-out",
};

function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

// Initial resting spot for a floating mascot: top-right, inset from the edges.
function defaultFloatPosition(size: number): { x: number; y: number } {
  if (typeof window === "undefined") return { x: FLOAT_MARGIN_PX, y: FLOAT_MARGIN_PX };
  return { x: window.innerWidth - size - FLOAT_MARGIN_PX, y: FLOAT_MARGIN_PX * 3 };
}

export function Mascot({
  size = 120,
  className,
  label = "workspace mascot",
  floating = false,
}: {
  size?: number;
  className?: string;
  label?: string;
  floating?: boolean;
}) {
  const buttonRef = useRef<HTMLButtonElement>(null);
  const bobRef = useRef<HTMLSpanElement>(null);
  const squashRef = useRef<HTMLSpanElement>(null);
  const timersRef = useRef<number[]>([]);
  const boopsRef = useRef({ count: 0, at: 0 });
  // Live drag session; `moved` gates the press between a boop and a drag.
  const dragRef = useRef<{ startX: number; startY: number; originX: number; originY: number; moved: boolean } | null>(null);
  const [direction, setDirection] = useState<Direction>("center");
  const [reaction, setReaction] = useState<Reaction | null>(null);
  const [pos, setPos] = useState(() => defaultFloatPosition(size));
  const [dragging, setDragging] = useState(false);
  // Touch users get a static, centered mascot: no pointer to follow, and a
  // hover-driven boop would never fire. Tapping still triggers the reaction.
  const coarse = useCoarsePointer();

  useEffect(() => {
    if (coarse) return;
    const aim = (event: PointerEvent) => {
      // Never turn the head to chase the very pointer that is dragging it.
      if (dragRef.current?.moved) return;
      const el = buttonRef.current;
      if (!el) return;
      const rect = el.getBoundingClientRect();
      const dx = event.clientX - (rect.left + rect.width / 2);
      const dy = event.clientY - (rect.top + rect.height / 2);
      if (Math.hypot(dx, dy) < AIM_DEADZONE_PX) {
        setDirection("center");
        return;
      }
      // Screen y grows downward; negate so "up" means above the mascot.
      setDirection(directionForAngle(Math.atan2(-dy, dx)));
    };
    window.addEventListener("pointermove", aim, { passive: true });
    return () => window.removeEventListener("pointermove", aim);
  }, [coarse]);

  useEffect(() => {
    const timers = timersRef.current;
    return () => timers.forEach(window.clearTimeout);
  }, []);

  // Keep the floating mascot on-screen when the viewport resizes.
  useEffect(() => {
    if (!floating) return;
    const onResize = () => {
      setPos((p) => ({
        x: clamp(p.x, FLOAT_MARGIN_PX, window.innerWidth - size - FLOAT_MARGIN_PX),
        y: clamp(p.y, FLOAT_MARGIN_PX, window.innerHeight - size - FLOAT_MARGIN_PX),
      }));
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [floating, size]);

  // Gentle idle bob while floating, unless the user is dragging or has asked
  // for reduced motion. Runs on its own wrapper so it never fights the boop.
  useEffect(() => {
    if (!floating || dragging || prefersReducedMotion()) return;
    const el = bobRef.current;
    if (!el) return;
    const anim = el.animate(
      [{ transform: "translateY(0px)" }, { transform: "translateY(-6px)" }],
      { duration: 2800, iterations: Infinity, direction: "alternate", easing: "ease-in-out" },
    );
    return () => anim.cancel();
  }, [floating, dragging]);

  const boop = () => {
    timersRef.current.forEach(window.clearTimeout);
    timersRef.current = [];
    const later = (ms: number, next: Reaction | null) => {
      timersRef.current.push(window.setTimeout(() => setReaction(next), ms));
    };
    const now = Date.now();
    const boops = boopsRef.current;
    boops.count = now - boops.at < DIZZY_WINDOW_MS ? boops.count + 1 : 1;
    boops.at = now;
    if (boops.count >= DIZZY_AFTER) {
      boops.count = 0;
      setReaction("dizzy");
      later(DIZZY_END_MS, null);
    } else {
      setReaction("blink");
      later(BOOP_PAYOFF_MS, PAYOFFS[(boops.count - 1) % PAYOFFS.length]);
      later(BOOP_END_MS, null);
    }
    if (prefersReducedMotion()) return;
    squashRef.current?.animate(
      [
        { transform: "scale(1, 1)" },
        { transform: "scale(1.08, 0.92)" },
        { transform: "scale(0.96, 1.04)" },
        { transform: "scale(1, 1)" },
      ],
      { duration: 420, easing: "linear" },
    );
  };

  const onPointerDown = (e: React.PointerEvent<HTMLButtonElement>) => {
    if (!floating) return;
    buttonRef.current?.setPointerCapture(e.pointerId);
    dragRef.current = { startX: e.clientX, startY: e.clientY, originX: pos.x, originY: pos.y, moved: false };
  };

  const onPointerMove = (e: React.PointerEvent<HTMLButtonElement>) => {
    const drag = dragRef.current;
    if (!drag) return;
    const dx = e.clientX - drag.startX;
    const dy = e.clientY - drag.startY;
    if (!drag.moved && Math.hypot(dx, dy) > DRAG_THRESHOLD_PX) {
      drag.moved = true;
      setDragging(true);
      setDirection("center");
    }
    if (!drag.moved) return;
    setPos({
      x: clamp(drag.originX + dx, FLOAT_MARGIN_PX, window.innerWidth - size - FLOAT_MARGIN_PX),
      y: clamp(drag.originY + dy, FLOAT_MARGIN_PX, window.innerHeight - size - FLOAT_MARGIN_PX),
    });
  };

  const endDrag = (e: React.PointerEvent<HTMLButtonElement>) => {
    const drag = dragRef.current;
    if (!drag) return;
    buttonRef.current?.releasePointerCapture(e.pointerId);
    dragRef.current = null;
    setDragging(false);
    // A press that never crossed the drag threshold is a boop, not a move.
    if (!drag.moved) boop();
  };

  const floatStyle: React.CSSProperties = floating
    ? { position: "fixed", left: pos.x, top: pos.y, zIndex: 50, touchAction: "none", cursor: dragging ? "grabbing" : "grab" }
    : {};

  return (
    <button
      ref={buttonRef}
      type="button"
      onClick={floating ? undefined : boop}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      aria-label={label}
      className={cn("relative block flex-shrink-0 cursor-pointer border-0 bg-transparent p-0 select-none", className)}
      style={{ width: size, height: size, ...floatStyle }}
    >
      <span ref={bobRef} className="relative block h-full w-full">
        <span
          ref={squashRef}
          className="relative block h-full w-full"
          style={{ transformOrigin: "50% 78%" }}
        >
          <span
            style={{
              ...layer,
              backgroundImage: `url(${directionsSheet})`,
              ...cell(DIRECTIONS.indexOf(direction)),
              opacity: reaction ? 0 : 1,
            }}
          />
          <span
            style={{
              ...layer,
              backgroundImage: `url(${reactionsSheet})`,
              ...cell(REACTIONS.indexOf(reaction ?? "blink")),
              opacity: reaction ? 1 : 0,
            }}
          />
        </span>
      </span>
    </button>
  );
}
