// Pure geometry of the image viewer (no DOM; unit-tested under node). The
// view places the image's top-left corner at (x, y) in container pixels and
// draws it at `scale` (1 = one image pixel per CSS pixel).

export interface Size {
  readonly w: number;
  readonly h: number;
}

export interface ImageView {
  readonly scale: number;
  readonly x: number;
  readonly y: number;
}

export const MIN_SCALE = 0.01;
export const MAX_SCALE = 64;
/** Pixel grid is offered from this zoom on (a cell is ≥ 4 CSS px). */
export const GRID_MIN_SCALE = 4;

export function clampScale(scale: number): number {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, scale));
}

/** Scale that fits the whole image inside the container (contain). */
export function fitScale(image: Size, box: Size): number {
  if (image.w <= 0 || image.h <= 0 || box.w <= 0 || box.h <= 0) return 1;
  return clampScale(Math.min(box.w / image.w, box.h / image.h));
}

/** The image centered in the container at a scale. */
export function centered(image: Size, box: Size, scale: number): ImageView {
  return { scale, x: (box.w - image.w * scale) / 2, y: (box.h - image.h * scale) / 2 };
}

/** Zooms to `scale` keeping the image point under (px, py) fixed. */
export function zoomAt(view: ImageView, scale: number, px: number, py: number): ImageView {
  const next = clampScale(scale);
  const ix = (px - view.x) / view.scale;
  const iy = (py - view.y) / view.scale;
  return { scale: next, x: px - ix * next, y: py - iy * next };
}

/** The visible part of the image (container coordinates) and the grid's
 *  background offset there, so the pixel-grid overlay stays viewport-sized
 *  however large the zoomed image is. Null when nothing is visible. */
export function visibleGrid(view: ImageView, image: Size, box: Size): { left: number; top: number; width: number; height: number; offsetX: number; offsetY: number } | null {
  const left = Math.max(0, view.x);
  const top = Math.max(0, view.y);
  const right = Math.min(box.w, view.x + image.w * view.scale);
  const bottom = Math.min(box.h, view.y + image.h * view.scale);
  if (right <= left || bottom <= top) return null;
  const mod = (n: number) => ((n % view.scale) + view.scale) % view.scale;
  return { left, top, width: right - left, height: bottom - top, offsetX: mod(view.x - left), offsetY: mod(view.y - top) };
}

/** An area of the image, in image pixels (top-left origin). */
export interface Region {
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
}

/** The image pixel under a container point, clamped to the image. */
export function toImage(view: ImageView, image: Size, px: number, py: number): { x: number; y: number } {
  const x = (px - view.x) / view.scale;
  const y = (py - view.y) / view.scale;
  return { x: Math.min(image.w, Math.max(0, Math.round(x))), y: Math.min(image.h, Math.max(0, Math.round(y))) };
}

/** The area two image points span (either order). */
export function spanRegion(a: { x: number; y: number }, b: { x: number; y: number }): Region {
  return { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), w: Math.abs(a.x - b.x), h: Math.abs(a.y - b.y) };
}

/** The type a crop is saved as: the image's own when the browser encodes
 *  it (PNG, JPEG, WebP), else PNG (a GIF's first frame, an SVG's
 *  rendering). */
export function cropMimeType(source: string): string {
  return source === "image/jpeg" || source === "image/webp" ? source : "image/png";
}

/** Where an area shows in the container (CSS pixels). */
export function regionRect(view: ImageView, r: Region): Region {
  return { x: view.x + r.x * view.scale, y: view.y + r.y * view.scale, w: r.w * view.scale, h: r.h * view.scale };
}

/** The selection an area makes: a short label, and the coordinates the
 *  agent works from. */
export function regionSelection(r: Region, image: Size, labels: readonly string[] = []): { label: string; detail: string } {
  return {
    label: `Area ${r.w}×${r.h} at (${r.x}, ${r.y})`,
    detail:
      `The rectangle from (${r.x}, ${r.y}) to (${r.x + r.w}, ${r.y + r.h}) in image pixels, ${r.w}×${r.h}, ` +
      `of the ${image.w}×${image.h} image (origin at the top-left).` +
      // The text inside, for a drawing that has any (lib/svgLabels.ts).
      (labels.length ? ` Labels in it: ${labels.map((l) => `“${l}”`).join(", ")}.` : ""),
  };
}
