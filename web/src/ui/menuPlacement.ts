// The menu surface's placement (ui/Menu.tsx), pure so it is tested alone.

// The gap kept between an open surface and the window's edges, and between
// the surface and its trigger.
const EDGE = 8;
const GAP = 4;

/** Where an open surface goes: the surface is portalled to the document body
 *  (fixed position), so no ancestor that clips or scrolls (a narrow rail, a
 *  dock panel) can cut it. Its position class still says where it prefers to
 *  open relative to its trigger: above or below (`bottom-full` / `top-full`),
 *  aligned to the trigger's left or right edge (`left-0` / `right-0`). It
 *  flips to the other side when the preferred one has no room, slides along
 *  the window's edge, and is never wider or taller than the window. */
export function placeSurface(
  trigger: { readonly left: number; readonly right: number; readonly top: number; readonly bottom: number },
  surface: { readonly width: number; readonly height: number },
  view: { readonly width: number; readonly height: number },
  prefer: { readonly above: boolean; readonly alignRight: boolean },
): { left: number; top: number; maxWidth: number; maxHeight: number } {
  const maxWidth = Math.max(120, view.width - 2 * EDGE);
  const w = Math.min(surface.width, maxWidth);
  let left = prefer.alignRight ? trigger.right - w : trigger.left;
  if (left + w > view.width - EDGE) left = view.width - EDGE - w;
  if (left < EDGE) left = EDGE;
  const below = view.height - trigger.bottom - GAP - EDGE;
  const above = trigger.top - GAP - EDGE;
  const openAbove = prefer.above ? above >= surface.height || above >= below : below < surface.height && above > below;
  const room = Math.max(120, openAbove ? above : below);
  const h = Math.min(surface.height, room);
  const top = openAbove ? trigger.top - GAP - h : trigger.bottom + GAP;
  return { left, top, maxWidth, maxHeight: room };
}

/** The surface's preferred side and alignment, read from its position class
 *  (the classes every caller already passes). */
export function preferredPlacement(cls: string): { above: boolean; alignRight: boolean } {
  return { above: /\bbottom-full\b/.test(cls), alignRight: /\bright-0\b/.test(cls) };
}

/** The position class without what the portal decides (position, side,
 *  offsets), keeping the caller's width and height limits. */
export function sizeClasses(cls: string): string {
  return cls
    .split(/\s+/)
    .filter((c) => c && !/^(absolute|relative|fixed|(top|bottom|left|right)-\S+|m[tblrxy]?-\S+)$/.test(c))
    .join(" ");
}

