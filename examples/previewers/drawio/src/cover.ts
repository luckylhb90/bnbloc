// The file's card picture (capability "thumbnail"): the first page as a
// schematic, drawn from the cells' geometry on the room's cover canvas:
// boxes (rectangles, rounded rectangles, ellipses, rhombi) in their own
// colors, arrows as lines with a head, short labels. Not maxGraph's drawing:
// that holds labels in foreignObjects, which taint a canvas in WebKit, and a
// card at 240 px has no use for shadows and stencils. The layout is pure
// (unit-tested under node); drawing needs a canvas.

import { labelText, type Cell, type Page } from "./model.ts";
import { isContainer, normalizeStyle, parseStyle, shapeOf } from "./style.ts";

export interface CoverBox {
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
  readonly shape: "rect" | "ellipse" | "rhombus";
  readonly rounded: boolean;
  /** A lane or group: its label goes on top, its content is inside it. */
  readonly container: boolean;
  readonly fill: string | null;
  readonly stroke: string | null;
  readonly label: string;
}

export interface CoverEdge {
  readonly points: readonly { readonly x: number; readonly y: number }[];
  readonly arrow: boolean;
}

export interface CoverLayout {
  readonly boxes: readonly CoverBox[];
  readonly edges: readonly CoverEdge[];
  /** The extent of everything, in the page's own units. */
  readonly bounds: { readonly x: number; readonly y: number; readonly w: number; readonly h: number };
}

const LABEL_CHARS = 28;
const MAX_CELLS = 600;

const clip = (text: string): string => {
  const first = text.split("\n")[0] ?? "";
  const chars = [...first];
  return chars.length > LABEL_CHARS ? `${chars.slice(0, LABEL_CHARS - 1).join("")}…` : first;
};

/** A color the style names: "none" is no paint, an unnamed one the default. */
function paint(value: string | undefined, fallback: string): string | null {
  if (value === undefined) return fallback;
  return value === "none" ? null : value;
}

type Point = { x: number; y: number };
type Frame = { x: number; y: number; w: number; h: number };

/** Where the line from inside `box` towards `toward` leaves the box: an arrow
 *  ends at its box's edge, not at its centre, where the box's own fill would
 *  hide the head. */
export function leaveBox(box: Frame, toward: Point): Point {
  const cx = box.x + box.w / 2;
  const cy = box.y + box.h / 2;
  const dx = toward.x - cx;
  const dy = toward.y - cy;
  if (dx === 0 && dy === 0) return { x: cx, y: cy };
  const t = Math.min(dx !== 0 ? box.w / 2 / Math.abs(dx) : Infinity, dy !== 0 ? box.h / 2 / Math.abs(dy) : Infinity, 1);
  return { x: cx + dx * t, y: cy + dy * t };
}

/** The page's cells as boxes and arrows in absolute coordinates (a cell inside
 *  a container is placed by its container), or null when it has nothing to
 *  draw. */
export function coverLayout(page: Page): CoverLayout | null {
  const cells = page.cells.slice(0, MAX_CELLS);
  const byId = new Map(cells.map((c) => [c.id, c]));
  const origin = (cell: Cell): { x: number; y: number } => {
    let x = 0;
    let y = 0;
    for (let at = byId.get(cell.parent), hops = 0; at && at.kind === "vertex" && hops < 20; at = byId.get(at.parent), hops++) {
      x += at.geometry?.x ?? 0;
      y += at.geometry?.y ?? 0;
    }
    return { x, y };
  };
  const frame = (cell: Cell) => {
    const g = cell.geometry;
    if (!g || g.relative || !(g.width && g.height)) return null;
    const o = origin(cell);
    return { x: (g.x ?? 0) + o.x, y: (g.y ?? 0) + o.y, w: g.width, h: g.height };
  };
  const centre = (cell: Cell | undefined) => {
    const f = cell && frame(cell);
    return f ? { x: f.x + f.w / 2, y: f.y + f.h / 2 } : null;
  };

  const boxes: CoverBox[] = [];
  for (const cell of cells) {
    if (cell.kind !== "vertex" || !cell.visible) continue;
    const f = frame(cell);
    if (!f) continue;
    const style = parseStyle(normalizeStyle(cell.style));
    const name = shapeOf(cell.style);
    const shape = /ellipse|cloud|circle/i.test(name) ? "ellipse" : /rhombus|decision/i.test(name) ? "rhombus" : "rect";
    boxes.push({
      ...f,
      shape,
      rounded: style.props.get("rounded") === "1",
      container: isContainer(cell.style),
      fill: paint(style.props.get("fillColor"), "#ffffff"),
      stroke: paint(style.props.get("strokeColor"), "#000000"),
      label: clip(labelText(cell)),
    });
  }

  const edges: CoverEdge[] = [];
  for (const cell of cells) {
    if (cell.kind !== "edge" || !cell.visible) continue;
    const g = cell.geometry;
    const o = origin(cell);
    const src = byId.get(cell.source ?? "");
    const dst = byId.get(cell.target ?? "");
    const from = centre(src) ?? (g?.sourcePoint ? { x: g.sourcePoint.x + o.x, y: g.sourcePoint.y + o.y } : null);
    const to = centre(dst) ?? (g?.targetPoint ? { x: g.targetPoint.x + o.x, y: g.targetPoint.y + o.y } : null);
    if (!from || !to) continue;
    const bends = (g?.points ?? []).map((p) => ({ x: p.x + o.x, y: p.y + o.y }));
    const path = [from, ...bends, to];
    const srcFrame = src && frame(src);
    const dstFrame = dst && frame(dst);
    if (srcFrame) path[0] = leaveBox(srcFrame, path[1]);
    if (dstFrame) path[path.length - 1] = leaveBox(dstFrame, path[path.length - 2]);
    edges.push({ points: path, arrow: parseStyle(cell.style).props.get("endArrow") !== "none" });
  }

  const xs = [...boxes.flatMap((b) => [b.x, b.x + b.w]), ...edges.flatMap((e) => e.points.map((p) => p.x))];
  const ys = [...boxes.flatMap((b) => [b.y, b.y + b.h]), ...edges.flatMap((e) => e.points.map((p) => p.y))];
  if (boxes.length === 0 || xs.length === 0) return null;
  const x = Math.min(...xs);
  const y = Math.min(...ys);
  return { boxes, edges, bounds: { x, y, w: Math.max(...xs) - x, h: Math.max(...ys) - y } };
}

/** The scale and offset that centre the layout in a w × h cover with a margin
 *  (never above 1.5: a small diagram is not blown up into a blur). */
export function fitCover(bounds: CoverLayout["bounds"], w: number, h: number, pad = 14): { k: number; ox: number; oy: number } {
  const k = Math.min((w - 2 * pad) / Math.max(1, bounds.w), (h - 2 * pad) / Math.max(1, bounds.h), 1.5);
  return { k, ox: (w - bounds.w * k) / 2 - bounds.x * k, oy: (h - bounds.h * k) / 2 - bounds.y * k };
}

/** Draws the layout on a cover canvas. */
export function drawCover(ctx: CanvasRenderingContext2D, layout: CoverLayout, w: number, h: number): void {
  const { k, ox, oy } = fitCover(layout.bounds, w, h);
  const X = (x: number) => x * k + ox;
  const Y = (y: number) => y * k + oy;
  ctx.lineJoin = "round";
  for (const e of layout.edges) {
    ctx.strokeStyle = "#555555";
    ctx.fillStyle = "#555555";
    ctx.lineWidth = Math.max(1, k);
    ctx.beginPath();
    e.points.forEach((p, i) => (i === 0 ? ctx.moveTo(X(p.x), Y(p.y)) : ctx.lineTo(X(p.x), Y(p.y))));
    ctx.stroke();
    if (e.arrow && e.points.length >= 2) {
      const a = e.points[e.points.length - 2];
      const b = e.points[e.points.length - 1];
      const ang = Math.atan2(Y(b.y) - Y(a.y), X(b.x) - X(a.x));
      const s = Math.max(5, 7 * k);
      ctx.beginPath();
      ctx.moveTo(X(b.x), Y(b.y));
      ctx.lineTo(X(b.x) - s * Math.cos(ang - 0.4), Y(b.y) - s * Math.sin(ang - 0.4));
      ctx.lineTo(X(b.x) - s * Math.cos(ang + 0.4), Y(b.y) - s * Math.sin(ang + 0.4));
      ctx.closePath();
      ctx.fill();
    }
  }
  // Boxes over the lines (a box's own fill hides the arrow's stem inside it).
  for (const b of layout.boxes) {
    const x = X(b.x);
    const y = Y(b.y);
    const bw = b.w * k;
    const bh = b.h * k;
    ctx.beginPath();
    if (b.shape === "ellipse") ctx.ellipse(x + bw / 2, y + bh / 2, bw / 2, bh / 2, 0, 0, Math.PI * 2);
    else if (b.shape === "rhombus") {
      ctx.moveTo(x + bw / 2, y);
      ctx.lineTo(x + bw, y + bh / 2);
      ctx.lineTo(x + bw / 2, y + bh);
      ctx.lineTo(x, y + bh / 2);
      ctx.closePath();
    } else if (b.rounded) ctx.roundRect(x, y, bw, bh, Math.min(8 * k, bh / 3));
    else ctx.rect(x, y, bw, bh);
    if (b.fill) {
      ctx.fillStyle = b.fill;
      ctx.fill();
    }
    if (b.stroke) {
      ctx.strokeStyle = b.stroke;
      ctx.lineWidth = Math.max(1, k);
      ctx.stroke();
    }
    const size = Math.min(13, Math.max(0, 12 * k));
    if (b.label && size >= 6 && bw > 20) {
      ctx.font = `${size}px system-ui, -apple-system, "Segoe UI", sans-serif`;
      ctx.fillStyle = "#222222";
      ctx.textAlign = "center";
      ctx.textBaseline = b.container ? "top" : "middle";
      ctx.fillText(b.label, x + bw / 2, b.container ? y + 4 * k : y + bh / 2, bw - 6);
    }
  }
}
