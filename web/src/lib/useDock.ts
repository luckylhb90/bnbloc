import {
  useCallback,
  useEffect,
  useState,
  type CSSProperties,
  type RefObject,
} from "react";
import type { Stage } from "@/lib/capability";
import { startPointerDrag } from "@/lib/pointerDrag";
import { StorageKey, readStored, writeStored } from "./storageKeys.ts";

export type DockStyle = CSSProperties & {
  readonly "--dock-w": string;
};

// How the dock presents at the current stage: an inline resizable column at
// wide, a floating overlay at medium, and a full-surface overlay at compact.
export type DockMode = "inline" | "overlay" | "full";

export interface UseDock {
  // Presentation for the current stage, and the derived open/inline/overlay
  // gates the room layout switches on.
  dockMode: DockMode;
  dockIsInline: boolean;
  dockIsOverlay: boolean;
  // Whether a resize drag is in progress (the inline aside disables its width
  // transition while true so it tracks the pointer 1:1).
  draggingDock: boolean;
  // The typed style object carrying the --dock-w custom property.
  dockStyle: DockStyle;
  // Begin a pointer-driven resize of the inline dock.
  startDockDrag: (e: React.PointerEvent) => void;
}

const DEFAULT_DOCK_SHARE = 0.36;

function dockShareFromStorage(): number {
  const saved = Number(readStored(StorageKey.dockShare));
  return Number.isFinite(saved) && saved > 0 ? saved : DEFAULT_DOCK_SHARE;
}

// useDock owns the preview dock's width model, drag + persistence, open
// animation, and the body-class side effect. It is pure UI state with no
// coupling to the message feed or to what the dock shows (the workbench owns
// its tabs and whether it is open). The stage is passed in (the room
// measures it once via useStage) so the hook never measures layout itself;
// roomRowRef is the drag reference frame, shared with the room's layout row.
// The dock renders beside the chat column — chat is never replaced; a member
// mid-conversation must not be yanked into a different surface.
export function useDock(
  roomStage: Stage,
  roomRowRef: RefObject<HTMLDivElement | null>,
  open: boolean,
): UseDock {
  const dockMode: DockMode =
    roomStage === "wide" ? "inline" : roomStage === "medium" ? "overlay" : "full";
  const dockIsInline = open && dockMode === "inline";
  const dockIsOverlay = open && dockMode !== "inline";

  const [dockShare, setDockShare] = useState(dockShareFromStorage);
  // Drag smoothness: pointermove only updates state (no storage writes — a
  // synchronous localStorage hit per move event is what made dragging feel
  // sticky); the preference is persisted once, on release. The width
  // transition is disabled while dragging (see the aside className) so the
  // dock tracks the pointer 1:1 instead of easing behind it.
  const [draggingDock, setDraggingDock] = useState(false);
  const startDockDrag = useCallback(
    (e: React.PointerEvent) => {
      setDraggingDock(true);
      let last = -1;
      startPointerDrag(
        e,
        (ev) => {
          const row = roomRowRef.current?.getBoundingClientRect();
          if (!row || row.width <= 0) return;
          const next = Math.min(1, Math.max(0, (row.right - ev.clientX) / row.width));
          if (Math.abs(next - last) > 0.001) {
            last = next;
            setDockShare(next);
          }
        },
        () => {
          setDraggingDock(false);
          if (last >= 0) writeStored(StorageKey.dockShare, last.toFixed(4));
        },
      );
    },
    [roomRowRef],
  );
  // Entry animation: the dock mounts at width 0 and grows to its target on
  // the next frame, so opening reads as a slide-in rather than a layout pop.
  const [dockOpened, setDockOpened] = useState(false);
  const dockStyle: DockStyle = {
    "--dock-w":
      dockOpened || draggingDock
        ? `clamp(min(18rem, 35%), ${dockShare * 100}%, min(42rem, 55%))`
        : "0px",
  };
  useEffect(() => {
    if (!open) {
      setDockOpened(false);
      return;
    }
    setDockOpened(false);
    let raf2 = 0;
    const raf1 = requestAnimationFrame(() => {
      raf2 = requestAnimationFrame(() => setDockOpened(true));
    });
    return () => {
      cancelAnimationFrame(raf1);
      cancelAnimationFrame(raf2);
    };
  }, [open]);
  // While a preview is docked the chat column is under space pressure: tell
  // the shell (via a body class) to shed its sidebar labels, and hide the
  // room's own header affordances' slack. Removed on undock.
  useEffect(() => {
    document.body.classList.toggle("dock-open", dockIsInline);
    return () => document.body.classList.remove("dock-open");
  }, [dockIsInline]);

  return {
    dockMode,
    dockIsInline,
    dockIsOverlay,
    draggingDock,
    dockStyle,
    startDockDrag,
  };
}
