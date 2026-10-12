import { useEffect, useRef, type RefObject } from "react";

// useDismissOnOutside closes an open popover on a mouse press outside ref
// (and, with escape, on the Escape key). The one copy of what every picker
// and popover did by hand (web L8). close may be an inline arrow: the
// listeners always call the latest one.
export function useDismissOnOutside(
  open: boolean,
  ref: RefObject<HTMLElement | null>,
  close: () => void,
  { escape = false }: { readonly escape?: boolean } = {},
): void {
  const closeRef = useRef(close);
  closeRef.current = close;
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) closeRef.current();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") closeRef.current();
    };
    document.addEventListener("mousedown", onDown);
    if (escape) document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, ref, escape]);
}
