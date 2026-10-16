// A draw.io file read into pages of cells, and written back as the clean
// <mxGraphModel> maxGraph draws. A scanner over the XML, not a DOM, so it
// runs the same in the frame and under node tests. This is the one place
// that decides what a cell is (its id, its label, its ends) and what is
// refused: the rest of the previewer asks it.
//
// A file is <mxfile><diagram id name>…</diagram>…</mxfile>; each diagram is
// an <mxGraphModel> or that XML deflated, base64-encoded and URL-encoded. A
// bare <mxGraphModel> file is one page.

import { decodeXml } from "../../lib/ooxml.ts";
import { isHtmlStyle, normalizeStyle } from "./style.ts";

/** Bounds: a file is read at most this far, the rest is named, not drawn. */
export const MAX_CELLS = 5000;
export const MAX_PAGES = 50;
export const MAX_INFLATED_BYTES = 8 * 1024 * 1024;

export interface Point {
  readonly x: number;
  readonly y: number;
}

export interface Geometry {
  readonly x?: number;
  readonly y?: number;
  readonly width?: number;
  readonly height?: number;
  readonly relative: boolean;
  /** An edge's bends. */
  readonly points: readonly Point[];
  readonly sourcePoint?: Point;
  readonly targetPoint?: Point;
  readonly offset?: Point;
}

export interface Cell {
  readonly id: string;
  readonly parent: string;
  /** "layer": a cell that is neither box nor arrow, directly under the root. */
  readonly kind: "vertex" | "edge" | "layer";
  /** The label as the file has it: HTML when `html` is set, else text. */
  readonly label: string;
  readonly html: boolean;
  readonly style: string;
  readonly source?: string;
  readonly target?: string;
  readonly geometry?: Geometry;
  readonly visible: boolean;
  readonly collapsed: boolean;
}

export interface Page {
  readonly id: string;
  readonly name: string;
  /** The root cell's id (usually "0"). */
  readonly root: string;
  readonly cells: readonly Cell[];
  /** Cells beyond MAX_CELLS (or without a parent) that are not drawn. */
  readonly omitted: number;
}

export interface Drawing {
  readonly pages: readonly Page[];
  /** Pages beyond MAX_PAGES that are not read. */
  readonly omittedPages: number;
}

// --- scanning --------------------------------------------------------------

const ATTRS = String.raw`(?:\s+[\w:.-]+\s*=\s*(?:"[^"]*"|'[^']*'))*`;
const TAG = new RegExp(String.raw`<(/?)([A-Za-z][\w:.-]*)(${ATTRS})\s*(/?)>`, "g");
const OPEN_DIAGRAM = new RegExp(String.raw`<diagram(${ATTRS})\s*(/?)>`, "g");

function attrsOf(src: string): Record<string, string> {
  const out: Record<string, string> = Object.create(null);
  for (const m of src.matchAll(/([\w:.-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g)) out[m[1]] = decodeXml(m[2] ?? m[3] ?? "");
  return out;
}

function num(s: string | undefined): number | undefined {
  if (s === undefined || s === "") return undefined;
  const n = Number(s);
  return Number.isFinite(n) ? n : undefined;
}

function point(a: Record<string, string>): Point {
  return { x: num(a.x) ?? 0, y: num(a.y) ?? 0 };
}

interface Draft {
  readonly a: Record<string, string>;
  readonly wrapper: Record<string, string> | null;
  geometry: {
    x?: number;
    y?: number;
    width?: number;
    height?: number;
    relative: boolean;
    points: Point[];
    sourcePoint?: Point;
    targetPoint?: Point;
    offset?: Point;
  } | null;
}

function cellOf(d: Draft): Cell | null {
  const id = d.wrapper?.id ?? d.a.id;
  if (!id) return null;
  const parent = d.a.parent;
  const kind = d.a.vertex === "1" ? "vertex" : d.a.edge === "1" ? "edge" : parent !== undefined ? "layer" : null;
  if (kind === null) return null;
  const style = d.a.style ?? "";
  const label = d.wrapper ? (d.wrapper.label ?? d.a.value ?? "") : (d.a.value ?? "");
  return {
    id,
    parent: parent ?? "",
    kind,
    label,
    html: isHtmlStyle(style),
    style,
    ...(d.a.source ? { source: d.a.source } : {}),
    ...(d.a.target ? { target: d.a.target } : {}),
    ...(d.geometry ? { geometry: d.geometry } : {}),
    visible: d.a.visible !== "0",
    collapsed: d.a.collapsed === "1",
  };
}

/** The cells of one <mxGraphModel>, in the file's order. A cell wrapped in
 *  <object>/<UserObject> (draw.io's custom properties) takes its id and its
 *  label from the wrapper. */
function readCells(model: string): { root: string; cells: Cell[]; omitted: number } {
  const found: Cell[] = [];
  let root = "";
  let wrapper: Record<string, string> | null = null;
  let draft: Draft | null = null;
  let inPoints = false;

  const finish = () => {
    if (!draft) return;
    const cell = cellOf(draft);
    if (cell) found.push(cell);
    else if (!root && draft.a.parent === undefined && draft.a.id) root = draft.a.id;
    draft = null;
  };

  for (const m of model.matchAll(TAG)) {
    const [, closing, name, rawAttrs, selfClosing] = m;
    if (closing) {
      if (name === "mxCell") finish();
      else if (name === "object" || name === "UserObject") wrapper = null;
      else if (name === "Array") inPoints = false;
      continue;
    }
    if (name === "object" || name === "UserObject") {
      wrapper = attrsOf(rawAttrs);
    } else if (name === "mxCell") {
      draft = { a: attrsOf(rawAttrs), wrapper, geometry: null };
      if (selfClosing) finish();
    } else if (draft && name === "mxGeometry") {
      const a = attrsOf(rawAttrs);
      if (a.as === "geometry" || a.as === undefined) {
        draft.geometry = { x: num(a.x), y: num(a.y), width: num(a.width), height: num(a.height), relative: a.relative === "1", points: [] };
      }
    } else if (draft?.geometry && name === "Array") {
      inPoints = attrsOf(rawAttrs).as === "points" && !selfClosing;
    } else if (draft?.geometry && name === "mxPoint") {
      const a = attrsOf(rawAttrs);
      if (inPoints) draft.geometry.points.push(point(a));
      else if (a.as === "sourcePoint") draft.geometry.sourcePoint = point(a);
      else if (a.as === "targetPoint") draft.geometry.targetPoint = point(a);
      else if (a.as === "offset") draft.geometry.offset = point(a);
    }
  }
  finish();

  // First occurrence of an id wins; the cap keeps the file's first cells; a
  // cell whose parent is not drawn is not drawn.
  const seen = new Set<string>();
  const unique = found.filter((c) => !seen.has(c.id) && seen.add(c.id));
  const capped = unique.slice(0, MAX_CELLS);
  const ids = new Set(capped.map((c) => c.id));
  const cells = capped.filter((c) => c.parent === root || ids.has(c.parent));
  return { root: root || "0", cells, omitted: found.length - cells.length };
}

// --- reading a file --------------------------------------------------------

/** The XML of a compressed <diagram>: base64, raw deflate, URL-encoded. */
export async function inflateDiagram(data: string): Promise<string> {
  const binary = atob(data.replace(/\s+/g, ""));
  const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
  const reader = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("deflate-raw")).getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > MAX_INFLATED_BYTES) {
      await reader.cancel();
      throw new Error("the diagram is larger than 8 MiB once unpacked");
    }
    chunks.push(value);
  }
  const all = new Uint8Array(total);
  let at = 0;
  for (const c of chunks) {
    all.set(c, at);
    at += c.byteLength;
  }
  return decodeURIComponent(new TextDecoder().decode(all));
}

async function modelXml(body: string): Promise<string> {
  const text = body.trim();
  if (text === "" || text.startsWith("<")) return text;
  try {
    return await inflateDiagram(text);
  } catch (err) {
    if (err instanceof Error && err.message.includes("8 MiB")) throw err;
    throw new Error("the diagram's compressed data cannot be read");
  }
}

function pageOf(model: string, id: string, name: string): Page {
  const { root, cells, omitted } = readCells(model);
  return { id, name, root, cells, omitted };
}

/** Reads a .drawio file: its pages (at most MAX_PAGES), each with its cells
 *  (at most MAX_CELLS). Throws a message the member can read. */
export async function readDrawing(text: string): Promise<Drawing> {
  const xml = text.replace(/^\uFEFF/, "").replace(/<!--[\s\S]*?-->/g, "");
  if (!/<mxfile\b/.test(xml)) {
    if (!/<mxGraphModel\b/.test(xml)) throw new Error("not a draw.io file (no <mxfile> or <mxGraphModel>)");
    return { pages: [pageOf(xml, "page-1", "Page-1")], omittedPages: 0 };
  }
  const pages: Page[] = [];
  const ids = new Set<string>();
  let omittedPages = 0;
  for (const m of xml.matchAll(OPEN_DIAGRAM)) {
    if (pages.length >= MAX_PAGES) {
      omittedPages++;
      continue;
    }
    const a = attrsOf(m[1]);
    const start = (m.index ?? 0) + m[0].length;
    const end = xml.indexOf("</diagram>", start);
    const body = m[2] ? "" : xml.slice(start, end < 0 ? undefined : end);
    const n = pages.length + 1;
    const name = a.name || `Page-${n}`;
    let id = a.id || `page-${n}`;
    while (ids.has(id)) id += "~";
    ids.add(id);
    let model: string;
    try {
      model = await modelXml(body);
    } catch (err) {
      throw new Error(`Page “${name}”: ${err instanceof Error ? err.message : String(err)}`);
    }
    if (model !== "" && !/<mxGraphModel\b/.test(model)) throw new Error(`Page “${name}” is not a draw.io diagram`);
    pages.push(pageOf(model, id, name));
  }
  if (pages.length === 0) throw new Error("the file has no diagram");
  return { pages, omittedPages };
}

// --- text ------------------------------------------------------------------

/** A label as plain lines: tags dropped (a break or a block closing starts a
 *  line), entities decoded, runs of spaces folded, blank lines gone. */
export function labelText(cell: Pick<Cell, "label" | "html">): string {
  let s = cell.label;
  if (cell.html) {
    s = s
      .replace(/<br\s*\/?>|<\/(?:div|p|li|tr|h[1-6])>/gi, "\n")
      .replace(/<[^>]*>/g, "")
      .replace(/&nbsp;/gi, " ");
    s = decodeXml(s);
  }
  return s
    .split("\n")
    .map((line) => line.replace(/\s+/g, " ").trim())
    .filter(Boolean)
    .join("\n");
}

// --- the clean model maxGraph draws -----------------------------------------

function escapeAttr(s: string): string {
  return s
    .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\uFFFE\uFFFF]/g, "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/\n/g, "&#xa;")
    .replace(/\r/g, "&#xd;")
    .replace(/\t/g, "&#x9;");
}

const attr = (name: string, value: string | number | undefined): string => (value === undefined ? "" : ` ${name}="${escapeAttr(String(value))}"`);

function geometryXml(g: Geometry): string {
  const inner: string[] = [];
  if (g.points.length) inner.push(`<Array as="points">${g.points.map((p) => `<mxPoint x="${p.x}" y="${p.y}"/>`).join("")}</Array>`);
  for (const [as, p] of [["sourcePoint", g.sourcePoint], ["targetPoint", g.targetPoint], ["offset", g.offset]] as const) {
    if (p) inner.push(`<mxPoint as="${as}" x="${p.x}" y="${p.y}"/>`);
  }
  const head = `<mxGeometry${attr("x", g.x)}${attr("y", g.y)}${attr("width", g.width)}${attr("height", g.height)}${g.relative ? ' relative="1"' : ""} as="geometry"`;
  return inner.length ? `${head}>${inner.join("")}</mxGeometry>` : `${head}/>`;
}

/** The page as the <mxGraphModel> maxGraph imports: wrappers unwrapped,
 *  styles normalized, HTML labels through `sanitize` (the one place HTML from
 *  a file is cleaned), a terminal that is not drawn dropped. */
export function graphXml(page: Page, sanitize: (html: string) => string): string {
  const ids = new Set(page.cells.map((c) => c.id));
  const cells = page.cells.map((c) => {
    const value = c.html ? sanitize(c.label) : c.label;
    const flags = `${c.kind === "vertex" ? ' vertex="1"' : c.kind === "edge" ? ' edge="1"' : ""}${c.visible ? "" : ' visible="0"'}${c.collapsed ? ' collapsed="1"' : ""}`;
    const ends = `${c.source && ids.has(c.source) ? attr("source", c.source) : ""}${c.target && ids.has(c.target) ? attr("target", c.target) : ""}`;
    const open = `<mxCell${attr("id", c.id)}${attr("parent", c.parent)}${attr("value", value)}${attr("style", normalizeStyle(c.style))}${flags}${ends}`;
    return c.geometry ? `${open}>${geometryXml(c.geometry)}</mxCell>` : `${open}/>`;
  });
  return `<mxGraphModel><root><mxCell${attr("id", page.root)}/>${cells.join("")}</root></mxGraphModel>`;
}
