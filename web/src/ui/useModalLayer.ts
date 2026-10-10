import { useEffect, useRef, type RefObject } from "react";

// useModalLayer gives every modal surface (Dialog, drawers, overlays, the
// image lightbox) the same keyboard and focus contract:
//   - a layer stack, so Escape closes only the TOP-most open layer (a confirm
//     opened from a dialog closes alone, not together with its parent);
//   - initial focus inside the layer (an autoFocus child wins, else the first
//     focusable control that is not the close button, else the surface);
//   - a Tab focus trap that cycles within the layer;
//   - focus restored to whatever was focused before the layer opened;
//   - optional body scroll lock while open.

const layers: symbol[] = [];

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function focusables(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (el) => !el.hasAttribute("hidden") && el.getClientRects().length > 0,
  );
}

export function useModalLayer(
  surfaceRef: RefObject<HTMLElement | null>,
  options: {
    readonly open?: boolean;
    // Called on Escape when this layer is top-most; omit for non-dismissible.
    readonly onEscape?: () => void;
    readonly lockScroll?: boolean;
  },
) {
  const { open = true, lockScroll = true } = options;
  const onEscapeRef = useRef(options.onEscape);
  onEscapeRef.current = options.onEscape;

  useEffect(() => {
    if (!open) return;
    const id = Symbol("layer");
    layers.push(id);
    const restoreTo = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousOverflow = document.body.style.overflow;
    if (lockScroll) document.body.style.overflow = "hidden";

    const raf = requestAnimationFrame(() => {
      const surface = surfaceRef.current;
      if (!surface || surface.contains(document.activeElement)) return;
      const first = focusables(surface).find((el) => !el.hasAttribute("data-layer-close"));
      (first ?? surface).focus();
    });

    const onKey = (e: KeyboardEvent) => {
      if (layers[layers.length - 1] !== id) return;
      if (e.key === "Escape") {
        if (!onEscapeRef.current) return;
        e.preventDefault();
        onEscapeRef.current();
        return;
      }
      if (e.key !== "Tab") return;
      const surface = surfaceRef.current;
      if (!surface) return;
      const items = focusables(surface);
      if (items.length === 0) {
        e.preventDefault();
        surface.focus();
        return;
      }
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (!surface.contains(active)) {
        e.preventDefault();
        first.focus();
      } else if (e.shiftKey && active === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKey);

    return () => {
      cancelAnimationFrame(raf);
      document.removeEventListener("keydown", onKey);
      const at = layers.indexOf(id);
      if (at !== -1) layers.splice(at, 1);
      if (lockScroll) document.body.style.overflow = previousOverflow;
      if (restoreTo && restoreTo.isConnected) restoreTo.focus();
    };
  }, [open, lockScroll, surfaceRef]);
}
