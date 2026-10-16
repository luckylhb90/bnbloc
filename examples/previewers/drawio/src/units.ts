// What a diagram's cells are called, for the agent and for Compare. Pure:
// node tests read it. A cell's unit key is "<page id>/<cell id>" (stable when
// a box moves or others are added); its text is what the member would call a
// change: the label, the shape, an arrow's ends, never the geometry.

import { labelText, type Cell, type Drawing, type Page } from "./model.ts";
import { isContainer, parseStyle, shapeOf } from "./style.ts";

const QUOTE_LIMIT = 48;
const CONNECTED_LIMIT = 6;

export interface DrawingUnit {
  readonly key: string;
  readonly label: string;
  readonly text: string;
  /** "cell:<page id>/<cell id>" (main.ts reveals it). */
  readonly at: string;
}

export function cellKey(page: Page, cell: Cell): string {
  return `${page.id}/${cell.id}`;
}

/** Where a cell is, as a unit's `at`. */
export function cellAt(page: Page, cell: Cell): string {
  return `cell:${cellKey(page, cell)}`;
}

/** The cell a unit's `at` names: the page whose id starts it, longest first
 *  (an id may contain a slash). */
export function findCell(drawing: Drawing, at: string): { page: number; cell: Cell } | null {
  if (!at.startsWith("cell:")) return null;
  const rest = at.slice("cell:".length);
  const order = drawing.pages.map((p, i) => ({ p, i })).sort((a, b) => b.p.id.length - a.p.id.length);
  for (const { p, i } of order) {
    if (!rest.startsWith(`${p.id}/`)) continue;
    const id = rest.slice(p.id.length + 1);
    const cell = p.cells.find((c) => c.id === id);
    if (cell) return { page: i, cell };
  }
  return null;
}

function clip(text: string, limit = QUOTE_LIMIT): string {
  const flat = text.replace(/\s+/g, " ").trim();
  const chars = [...flat];
  return chars.length > limit ? `${chars.slice(0, limit - 1).join("").trimEnd()}…` : flat;
}

const quote = (text: string) => `“${clip(text)}”`;

/** What kind of part the cell is, as a member would say it. */
export function nounOf(cell: Cell): string {
  if (cell.kind === "edge") return "Arrow";
  if (cell.kind === "layer") return "Layer";
  if (isContainer(cell.style)) return "Group";
  if (parseStyle(cell.style).names.includes("text")) return "Text";
  return "Box";
}

/** "“Start”" for a cell with text, else its id. */
function nameOf(cell: Cell | undefined, id: string | undefined): string {
  if (!cell) return id ? `(${id})` : "(nothing)";
  const text = labelText(cell);
  return text ? quote(text) : `(${cell.id})`;
}

export function cellIndex(page: Page): Map<string, Cell> {
  return new Map(page.cells.map((c) => [c.id, c]));
}

/** An arrow's ends: "“Start” → “Check”". */
function endsOf(cell: Cell, index: Map<string, Cell>): string {
  return `${nameOf(index.get(cell.source ?? ""), cell.source)} → ${nameOf(index.get(cell.target ?? ""), cell.target)}`;
}

/** The units of a drawing: every box and arrow, in page order. */
export function drawingUnits(drawing: Drawing): DrawingUnit[] {
  const out: DrawingUnit[] = [];
  for (const page of drawing.pages) {
    const index = cellIndex(page);
    for (const cell of page.cells) {
      if (cell.kind === "layer") continue;
      const text = labelText(cell);
      const where = drawing.pages.length > 1 ? `${page.name}: ` : "";
      const noun = nounOf(cell);
      const body =
        cell.kind === "edge"
          ? [endsOf(cell, index), text].filter(Boolean).join(": ")
          : `${shapeOf(cell.style)}${text ? `: ${text}` : ""}`;
      out.push({
        key: cellKey(page, cell),
        label: `${where}${noun} ${text ? quote(text) : cell.kind === "edge" ? endsOf(cell, index) : `(${cell.id})`}`,
        text: body,
        at: cellAt(page, cell),
      });
    }
  }
  return out;
}

/** What pointing at a cell tells the agent: the chip, the detail, the keys. */
export function cellPoint(drawing: Drawing, pageIndex: number, cell: Cell): { label: string; detail: string; keys: string[] } {
  const page = drawing.pages[pageIndex];
  const index = cellIndex(page);
  const noun = nounOf(cell);
  const text = labelText(cell);
  const lines: string[] = [];
  if (text) lines.push(text);
  lines.push(`Cell id: ${cell.id}`);
  if (drawing.pages.length > 1) lines.push(`Page: ${quote(page.name)}`);
  if (cell.kind === "edge") {
    lines.push(`From: ${nameOf(index.get(cell.source ?? ""), cell.source)}`, `To: ${nameOf(index.get(cell.target ?? ""), cell.target)}`);
  } else {
    lines.push(`Shape: ${shapeOf(cell.style)}`);
    const names = (ids: string[]) => ids.slice(0, CONNECTED_LIMIT).map((id) => nameOf(index.get(id), id)).join(", ") + (ids.length > CONNECTED_LIMIT ? ", …" : "");
    const to = page.cells.filter((c) => c.kind === "edge" && c.source === cell.id && c.target).map((c) => c.target as string);
    const from = page.cells.filter((c) => c.kind === "edge" && c.target === cell.id && c.source).map((c) => c.source as string);
    if (to.length) lines.push(`Arrows to: ${names(to)}`);
    if (from.length) lines.push(`Arrows from: ${names(from)}`);
  }
  const chip = cell.kind === "edge" && !text ? `${noun}: ${endsOf(cell, index)}` : text ? `${noun}: ${quote(text)}` : `${noun} (${cell.id})`;
  return { label: chip, detail: lines.join("\n"), keys: [cellKey(page, cell)] };
}
