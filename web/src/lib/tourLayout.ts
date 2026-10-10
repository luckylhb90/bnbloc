// Geometry for the guided tour's spotlight and coach card (views/onboarding/
// TourOverlay). Pure, node-tested.

export interface Rect {
  readonly top: number;
  readonly left: number;
  readonly width: number;
  readonly height: number;
}

export interface Size {
  readonly width: number;
  readonly height: number;
}

// Below this viewport width the coach card is a sheet, not a floating card.
export const SHEET_BELOW = 640;
const MARGIN = 8;
const GAP = 12;
const PAD = 6;

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(v, Math.max(lo, hi)));

// spotlightRect pads the target and clips it to the viewport, so a target
// taller than the screen (a long feed) still gets a visible cutout. Null when
// nothing of it is on screen.
export function spotlightRect(target: Rect, view: Size): Rect | null {
  const top = Math.max(MARGIN / 2, target.top - PAD);
  const left = Math.max(MARGIN / 2, target.left - PAD);
  const bottom = Math.min(view.height - MARGIN / 2, target.top + target.height + PAD);
  const right = Math.min(view.width - MARGIN / 2, target.left + target.width + PAD);
  if (bottom - top < 4 || right - left < 4) return null;
  return { top, left, width: right - left, height: bottom - top };
}

export type CoachPlacement =
  | { readonly mode: "sheet"; readonly edge: "bottom" | "top" }
  | { readonly mode: "float"; readonly top: number; readonly left: number };

// placeCoach positions the coach card next to the spotlight: below, above,
// right or left of it when the card fits there, else inside the target's
// lower edge (a target that fills the screen), else centered with no target.
// On a narrow screen it is a bottom sheet — moved to the top only when the
// target sits where the bottom sheet would cover it (the composer).
export function placeCoach(spot: Rect | null, card: Size, view: Size): CoachPlacement {
  if (view.width < SHEET_BELOW) {
    if (!spot) return { mode: "sheet", edge: "bottom" };
    const coversBottom = spot.top + spot.height > view.height - card.height - MARGIN;
    const fitsAbove = spot.top >= card.height + MARGIN;
    return { mode: "sheet", edge: coversBottom && fitsAbove ? "top" : "bottom" };
  }
  const maxTop = view.height - card.height - MARGIN;
  const maxLeft = view.width - card.width - MARGIN;
  if (!spot) return { mode: "float", top: clamp((view.height - card.height) / 2, MARGIN, maxTop), left: clamp((view.width - card.width) / 2, MARGIN, maxLeft) };
  const centerLeft = clamp(spot.left + spot.width / 2 - card.width / 2, MARGIN, maxLeft);
  const below = spot.top + spot.height + GAP;
  if (below + card.height <= view.height - MARGIN) return { mode: "float", top: below, left: centerLeft };
  const above = spot.top - GAP - card.height;
  if (above >= MARGIN) return { mode: "float", top: above, left: centerLeft };
  const sideTop = clamp(spot.top, MARGIN, maxTop);
  const right = spot.left + spot.width + GAP;
  if (right + card.width <= view.width - MARGIN) return { mode: "float", top: sideTop, left: right };
  const left = spot.left - GAP - card.width;
  if (left >= MARGIN) return { mode: "float", top: sideTop, left };
  return { mode: "float", top: clamp(spot.top + spot.height - card.height - GAP * 2, MARGIN, maxTop), left: centerLeft };
}
