// The text labels inside a picked area of an SVG
// (docs/prd/2026-10-16-diagram-previewers.md rule 5). A region picked on a
// drawing is only a rectangle of pixels; naming the labels inside it tells
// the agent which boxes the member means.
//
// The viewer shows an SVG as an <img> (its markup never becomes app DOM), so
// there is no layout to ask. This reads the markup instead: each <text>'s
// anchor point through the <g transform>s above it and the root's viewBox,
// and counts a label when the middle of its (estimated) text box is inside
// the area. Approximate by design: no CSS classes, no <use>, no rotation, no
// foreignObject; a drawing that positions text any other way yields fewer
// labels, never wrong pixels. Pure (no DOM): unit-tested under node.

export interface Area {
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
}

export interface ImageSize {
  readonly w: number;
  readonly h: number;
}

/** Larger sources are not read (a member's area then stays a plain area). */
export const MAX_SVG_CHARS = 2 * 1024 * 1024;
export const MAX_LABELS = 40;
const MAX_LABELS_CHARS = 1200;
const DEFAULT_FONT_PX = 16;

type Matrix = readonly [number, number, number, number, number, number];
const IDENTITY: Matrix = [1, 0, 0, 1, 0, 0];

function multiply(m: Matrix, n: Matrix): Matrix {
  return [
    m[0] * n[0] + m[2] * n[1],
    m[1] * n[0] + m[3] * n[1],
    m[0] * n[2] + m[2] * n[3],
    m[1] * n[2] + m[3] * n[3],
    m[0] * n[4] + m[2] * n[5] + m[4],
    m[1] * n[4] + m[3] * n[5] + m[5],
  ];
}

const nums = (args: string): number[] =>
  args
    .split(/[\s,]+/)
    .filter(Boolean)
    .map(Number)
    .filter(Number.isFinite);

/** A transform attribute as one matrix (skew is not read). */
export function parseTransform(value: string | undefined): Matrix {
  let m = IDENTITY;
  for (const t of (value ?? "").matchAll(/(\w+)\s*\(([^)]*)\)/g)) {
    const a = nums(t[2]);
    let next: Matrix | null = null;
    switch (t[1]) {
      case "translate":
        next = [1, 0, 0, 1, a[0] ?? 0, a[1] ?? 0];
        break;
      case "scale":
        next = [a[0] ?? 1, 0, 0, a[1] ?? a[0] ?? 1, 0, 0];
        break;
      case "rotate": {
        const rad = ((a[0] ?? 0) * Math.PI) / 180;
        const rot: Matrix = [Math.cos(rad), Math.sin(rad), -Math.sin(rad), Math.cos(rad), 0, 0];
        next = a.length >= 3 ? multiply(multiply([1, 0, 0, 1, a[1], a[2]], rot), [1, 0, 0, 1, -a[1], -a[2]]) : rot;
        break;
      }
      case "matrix":
        if (a.length === 6) next = [a[0], a[1], a[2], a[3], a[4], a[5]];
        break;
    }
    if (next) m = multiply(m, next);
  }
  return m;
}

// --- scanning --------------------------------------------------------------

const ATTRS = String.raw`(?:\s+[\w:.-]+\s*=\s*(?:"[^"]*"|'[^']*'))*`;
const TAG = new RegExp(String.raw`<(/?)([A-Za-z][\w:.-]*)(${ATTRS})\s*(/?)>`, "g");
// Parts that draw nothing (or nothing of ours): their content is skipped.
const INERT = new Set(["defs", "symbol", "clipPath", "mask", "pattern", "marker", "style", "script", "metadata", "title", "desc", "foreignObject"]);

function attrsOf(src: string): Record<string, string> {
  const out: Record<string, string> = Object.create(null);
  for (const m of src.matchAll(/([\w:.-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g)) out[m[1]] = m[2] ?? m[3] ?? "";
  return out;
}

/** A presentation property: the style attribute's declaration over the
 *  attribute of the same name. */
function prop(a: Record<string, string>, name: string): string | undefined {
  const decl = new RegExp(`(?:^|;)\\s*${name}\\s*:\\s*([^;]+)`, "i").exec(a.style ?? "");
  return decl ? decl[1].trim() : a[name];
}

function decode(s: string): string {
  return s.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (m, e: string) => {
    if (e[0] === "#") {
      const code = e[1] === "x" || e[1] === "X" ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10);
      return Number.isFinite(code) && code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : m;
    }
    return { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " }[e.toLowerCase()] ?? m;
  });
}

const first = (v: string | undefined): number | undefined => {
  const n = parseFloat((v ?? "").trim().split(/[\s,]+/)[0] ?? "");
  return Number.isFinite(n) ? n : undefined;
};

interface Frame {
  readonly name: string;
  readonly matrix: Matrix;
  readonly fontSize: number;
  readonly anchor: string;
}

interface Label {
  readonly text: string;
  /** The middle of the text box, in the root's user units. */
  readonly cx: number;
  readonly cy: number;
}

/** The text's estimated width: a CJK or full-width character is one em, any
 *  other about half. */
function textWidth(text: string, fontSize: number): number {
  let em = 0;
  for (const ch of text) em += /[⺀-﹯＀-￯]/.test(ch) ? 1 : 0.55;
  return em * fontSize;
}

interface Root {
  readonly viewBox: readonly [number, number, number, number] | null;
  readonly align: string;
  readonly slice: boolean;
}

/** Where the root's user units land in the image's pixels: the viewBox fitted
 *  into the image box by preserveAspectRatio. Returns the pixel → unit map. */
function pixelToUnit(root: Root, natural: ImageSize): (px: number, py: number) => [number, number] {
  const vb = root.viewBox ?? [0, 0, natural.w, natural.h];
  const [vx, vy, vw, vh] = vb;
  if (root.align === "none" || vw <= 0 || vh <= 0) {
    const sx = vw > 0 ? natural.w / vw : 1;
    const sy = vh > 0 ? natural.h / vh : 1;
    return (px, py) => [vx + px / sx, vy + py / sy];
  }
  const s = root.slice ? Math.max(natural.w / vw, natural.h / vh) : Math.min(natural.w / vw, natural.h / vh);
  const free = (axis: string, room: number) => (axis === "Min" ? 0 : axis === "Max" ? room : room / 2);
  const ox = free(root.align.slice(1, 4), natural.w - vw * s);
  const oy = free(root.align.slice(5, 8), natural.h - vh * s);
  return (px, py) => [vx + (px - ox) / s, vy + (py - oy) / s];
}

function readRoot(a: Record<string, string>): Root {
  const box = (a.viewBox ?? "").trim() ? nums(a.viewBox) : [];
  const par = (a.preserveAspectRatio ?? "xMidYMid meet").trim().split(/\s+/);
  const align = par[0] === "none" ? "none" : /^x(Min|Mid|Max)Y(Min|Mid|Max)$/.test(par[0]) ? par[0] : "xMidYMid";
  return { viewBox: box.length === 4 ? [box[0], box[1], box[2], box[3]] : null, align, slice: par[1] === "slice" };
}

/** Every drawn label with where its middle is. */
function scanLabels(source: string): { root: Root; labels: Label[] } {
  const xml = source.replace(/<!--[\s\S]*?-->/g, "").replace(/<!\[CDATA\[([\s\S]*?)\]\]>/g, (_, t: string) => t.replace(/&/g, "&amp;").replace(/</g, "&lt;"));
  const stack: Frame[] = [{ name: "#root", matrix: IDENTITY, fontSize: DEFAULT_FONT_PX, anchor: "start" }];
  const labels: Label[] = [];
  let root: Root = { viewBox: null, align: "xMidYMid", slice: false };
  let seenRoot = false;
  // An element whose content is not read (inert or hidden), with how deep its own name nests inside it.
  let skip: { readonly name: string; depth: number } | null = null;
  let text: { frame: Frame; x: number | undefined; y: number | undefined; dx: number; dy: number; parts: string[] } | null = null;
  let last = 0;

  const top = () => stack[stack.length - 1];
  const finishText = () => {
    if (!text) return;
    const content = decode(text.parts.join("")).replace(/\s+/g, " ").trim();
    if (content) {
      const fs = text.frame.fontSize;
      const w = textWidth(content, fs);
      const dx = text.frame.anchor === "middle" ? 0 : text.frame.anchor === "end" ? -w / 2 : w / 2;
      const [cx, cy] = [(text.x ?? 0) + text.dx + dx, (text.y ?? 0) + text.dy - fs * 0.3];
      const m = text.frame.matrix;
      labels.push({ text: content, cx: m[0] * cx + m[2] * cy + m[4], cy: m[1] * cx + m[3] * cy + m[5] });
    }
    text = null;
  };

  for (const m of xml.matchAll(TAG)) {
    const [, closing, name, rawAttrs, selfClosing] = m;
    if (text) text.parts.push(xml.slice(last, m.index));
    last = (m.index ?? 0) + m[0].length;
    if (skip) {
      if (name === skip.name) {
        if (closing) {
          if (--skip.depth === 0) skip = null;
        } else if (!selfClosing) skip.depth++;
      }
      continue;
    }
    if (closing) {
      if (name === "text") finishText();
      else if (name === "tspan" || name === "textPath") {
        /* the text goes on */
      } else if (stack.length > 1 && top().name === name) stack.pop();
      continue;
    }
    if (INERT.has(name)) {
      if (!selfClosing) skip = { name, depth: 1 };
      continue;
    }
    const a = attrsOf(rawAttrs);
    if (text) {
      // A <tspan> in a <text>: its own position decides where a text without one sits.
      if (name === "tspan" && text.parts.join("").trim() === "") {
        const tx = first(a.x);
        const ty = first(a.y);
        if (tx !== undefined && text.x === undefined) text.x = tx;
        if (ty !== undefined && text.y === undefined) text.y = ty;
      }
      continue;
    }
    if (prop(a, "display") === "none" || prop(a, "visibility") === "hidden") {
      if (!selfClosing) skip = { name, depth: 1 };
      if (name === "svg") seenRoot = true;
      continue;
    }
    const parent = top();
    const fontSize = first(prop(a, "font-size")) ?? parent.fontSize;
    const anchor = prop(a, "text-anchor") ?? parent.anchor;
    if (name === "svg" && !seenRoot) {
      seenRoot = true;
      root = readRoot(a);
      if (!selfClosing) stack.push({ name, matrix: IDENTITY, fontSize, anchor });
      continue;
    }
    const frame: Frame = { name, matrix: multiply(parent.matrix, parseTransform(a.transform)), fontSize, anchor };
    if (name === "text") {
      // The scale on a text's own matrix scales its box, so the middle is found in its own space.
      text = { frame, x: first(a.x), y: first(a.y), dx: first(a.dx) ?? 0, dy: first(a.dy) ?? 0, parts: [] };
      if (selfClosing) finishText();
      continue;
    }
    if (!selfClosing) stack.push(frame);
  }
  return { root, labels };
}

/** The labels inside the area (image pixels of a `natural`-sized image), in
 *  reading order, each once, at most MAX_LABELS. */
export function svgLabelsIn(source: string, area: Area, natural: ImageSize): string[] {
  if (source.length > MAX_SVG_CHARS || natural.w <= 0 || natural.h <= 0) return [];
  const { root, labels } = scanLabels(source);
  if (labels.length === 0) return [];
  const toUnit = pixelToUnit(root, natural);
  const [x0, y0] = toUnit(area.x, area.y);
  const [x1, y1] = toUnit(area.x + area.w, area.y + area.h);
  const [left, right] = [Math.min(x0, x1), Math.max(x0, x1)];
  const [top, bottom] = [Math.min(y0, y1), Math.max(y0, y1)];
  const inside = labels
    .filter((l) => l.cx >= left && l.cx <= right && l.cy >= top && l.cy <= bottom)
    .sort((a, b) => a.cy - b.cy || a.cx - b.cx);
  const out: string[] = [];
  let chars = 0;
  for (const l of inside) {
    if (out.includes(l.text)) continue;
    if (out.length >= MAX_LABELS || chars + l.text.length > MAX_LABELS_CHARS) break;
    out.push(l.text);
    chars += l.text.length;
  }
  return out;
}
