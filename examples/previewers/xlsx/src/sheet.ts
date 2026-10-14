// The spreadsheet model the Excel previewer draws, built from an ExcelJS
// worksheet: what each cell shows (its value through its number format),
// how it looks (font, fill, borders, alignment), the merged ranges, column
// widths and row heights. Kept apart from the DOM so it is testable in Node.

import { format as formatNumber } from "numfmt";

/** The most a sheet draws: a browser table of more cells than this is too
 *  slow to scroll. Rows are cut first (a sheet is read top-down). */
export const MAX_CELLS = 150_000;
export const MAX_COLS = 200;

// --- ExcelJS shapes (the subset read here) ---------------------------------

export interface XColor {
  argb?: string;
  theme?: number;
  tint?: number;
  indexed?: number;
}
export interface XFont {
  name?: string;
  size?: number;
  bold?: boolean;
  italic?: boolean;
  underline?: boolean | string;
  strike?: boolean;
  color?: XColor;
}
export interface XFill {
  type?: string;
  pattern?: string;
  fgColor?: XColor;
}
export interface XBorderSide {
  style?: string;
  color?: XColor;
}
export interface XStyle {
  numFmt?: string;
  font?: XFont;
  fill?: XFill;
  alignment?: { horizontal?: string; vertical?: string; wrapText?: boolean; indent?: number };
  border?: { top?: XBorderSide; left?: XBorderSide; bottom?: XBorderSide; right?: XBorderSide };
}

// --- colours -----------------------------------------------------------------

/** The default Office theme: lt1, dk1, lt2, dk2, accent1…6, hlink, folHlink. */
const THEME = ["FFFFFF", "000000", "E7E6E6", "44546A", "4472C4", "ED7D31", "A5A5A5", "FFC000", "5B9BD5", "70AD47", "0563C1", "954F72"];

/** The legacy indexed palette (0–63; 64 is the system foreground). */
const INDEXED = [
  "000000", "FFFFFF", "FF0000", "00FF00", "0000FF", "FFFF00", "FF00FF", "00FFFF",
  "000000", "FFFFFF", "FF0000", "00FF00", "0000FF", "FFFF00", "FF00FF", "00FFFF",
  "800000", "008000", "000080", "808000", "800080", "008080", "C0C0C0", "808080",
  "9999FF", "993366", "FFFFCC", "CCFFFF", "660066", "FF8080", "0066CC", "CCCCFF",
  "000080", "FF00FF", "FFFF00", "00FFFF", "800080", "800000", "008080", "0000FF",
  "00CCFF", "CCFFFF", "CCFFCC", "FFFF99", "99CCFF", "FF99CC", "CC99FF", "FFCC99",
  "3366FF", "33CCCC", "99CC00", "FFCC00", "FF9900", "FF6600", "666699", "969696",
  "003366", "339966", "003300", "333300", "993300", "993366", "333399", "333333",
];

/** Applies an OOXML tint (-1 darker … 1 lighter) to one channel. */
function tintChannel(c: number, tint: number): number {
  return Math.round(tint < 0 ? c * (1 + tint) : c + (255 - c) * tint);
}

/** A CSS colour for an ExcelJS colour; null when it names none. */
export function cssColor(color: XColor | undefined): string | null {
  if (!color) return null;
  let hex: string | undefined;
  if (typeof color.argb === "string" && /^[0-9a-f]{8}$/i.test(color.argb)) hex = color.argb.slice(2);
  else if (typeof color.argb === "string" && /^[0-9a-f]{6}$/i.test(color.argb)) hex = color.argb;
  else if (typeof color.theme === "number") hex = THEME[color.theme];
  else if (typeof color.indexed === "number") hex = INDEXED[color.indexed];
  if (!hex) return null;
  const tint = typeof color.tint === "number" ? Math.max(-1, Math.min(1, color.tint)) : 0;
  const rgb = [0, 2, 4].map((i) => tintChannel(parseInt(hex.slice(i, i + 2), 16), tint));
  return `#${rgb.map((c) => c.toString(16).padStart(2, "0")).join("")}`;
}

// --- sizes -------------------------------------------------------------------

/** Excel column width (characters of the default font) in CSS pixels. */
export function colWidthPx(width: number | undefined): number {
  return Math.round((width ?? 8.43) * 7 + 5);
}

/** Excel row height (points) in CSS pixels. */
export function rowHeightPx(points: number | undefined): number {
  return Math.round((points ?? 15) * (4 / 3));
}

/** 1 → "A", 27 → "AA". */
export function colName(n: number): string {
  let s = "";
  for (let i = n; i > 0; i = Math.floor((i - 1) / 26)) s = String.fromCharCode(65 + ((i - 1) % 26)) + s;
  return s;
}

/** "B3:D5" → 1-based {top, left, bottom, right}; null when malformed. */
export function parseRange(range: string): { top: number; left: number; bottom: number; right: number } | null {
  const m = /^([A-Z]+)(\d+):([A-Z]+)(\d+)$/.exec(range.replace(/\$/g, "").toUpperCase());
  if (!m) return null;
  const col = (s: string) => [...s].reduce((n, ch) => n * 26 + ch.charCodeAt(0) - 64, 0);
  const a = { r: Number(m[2]), c: col(m[1]) };
  const b = { r: Number(m[4]), c: col(m[3]) };
  return { top: Math.min(a.r, b.r), left: Math.min(a.c, b.c), bottom: Math.max(a.r, b.r), right: Math.max(a.c, b.c) };
}

/** A cell range, 1-based and inclusive. */
export interface CellRange {
  readonly top: number;
  readonly left: number;
  readonly bottom: number;
  readonly right: number;
}

/** The smallest range holding both (a merged cell is its whole area). */
export function coverRange(a: CellRange, b: CellRange): CellRange {
  return { top: Math.min(a.top, b.top), left: Math.min(a.left, b.left), bottom: Math.max(a.bottom, b.bottom), right: Math.max(a.right, b.right) };
}

/** "Revenue!B3:D5", "'Q3 Sales'!B3" — how a formula names the range. */
export function rangeLabel(sheet: string, r: CellRange): string {
  const name = /^[A-Za-z_][A-Za-z0-9_.]*$/.test(sheet) ? sheet : `'${sheet.replace(/'/g, "''")}'`;
  const from = `${colName(r.left)}${r.top}`;
  const to = `${colName(r.right)}${r.bottom}`;
  return `${name}!${from === to ? from : `${from}:${to}`}`;
}

/** How rangeText reads the drawn sheet. */
export interface SheetReader {
  text(row: number, col: number): string;
  rowShown(row: number): boolean;
  colShown(col: number): boolean;
}

/** The range's shown values as tab-separated rows, skipping hidden rows
 *  and columns, cut once it passes limit characters (the selection's
 *  detail is bounded anyway; this keeps a whole-sheet drag cheap). */
export function rangeText(r: CellRange, sheet: SheetReader, limit: number): string {
  const lines: string[] = [];
  let size = 0;
  for (let y = r.top; y <= r.bottom && size <= limit; y++) {
    if (!sheet.rowShown(y)) continue;
    const cells: string[] = [];
    for (let x = r.left; x <= r.right; x++) if (sheet.colShown(x)) cells.push(sheet.text(y, x).replace(/[\t\n\r]+/g, " "));
    const line = cells.join("\t");
    lines.push(line);
    size += line.length + 1;
  }
  return lines.join("\n");
}

// --- values ------------------------------------------------------------------

const MS_PER_DAY = 86_400_000;
/** 1970-01-01 as an Excel serial (the 1900 date system). */
const UNIX_EPOCH_SERIAL = 25_569;

/** What a cell shows: its value (a formula's cached result) through its
 *  number format. Dates arrive from ExcelJS as UTC instants. */
export function displayText(value: unknown, numFmt: string | undefined): string {
  let v: unknown = value;
  if (v !== null && typeof v === "object" && !(v instanceof Date)) {
    const o = v as Record<string, unknown>;
    if ("result" in o) v = o.result;
    else if ("error" in o) return String(o.error);
    else if (Array.isArray(o.richText)) return o.richText.map((r: { text?: unknown }) => String(r.text ?? "")).join("");
    else if ("text" in o) return String(o.text ?? "");
    else if ("formula" in o || "sharedFormula" in o) return "";
  }
  if (v === null || v === undefined) return "";
  if (v !== null && typeof v === "object" && "error" in (v as object)) return String((v as { error: unknown }).error);
  const pattern = numFmt && numFmt.trim() !== "" ? numFmt : "General";
  if (v instanceof Date) {
    const serial = v.getTime() / MS_PER_DAY + UNIX_EPOCH_SERIAL;
    return formatNumber(pattern === "General" ? "yyyy-mm-dd" : pattern, serial, { throws: false, locale: "en" });
  }
  if (typeof v === "number" || typeof v === "boolean") return formatNumber(pattern, v, { throws: false, locale: "en" });
  return String(v);
}

/** Inline CSS for a cell's style ("" when it has none). */
export function cellCss(style: XStyle | undefined, numeric: boolean): string {
  const css: string[] = [];
  const f = style?.font;
  if (f) {
    if (f.bold) css.push("font-weight:700");
    if (f.italic) css.push("font-style:italic");
    const lines = [f.underline ? "underline" : "", f.strike ? "line-through" : ""].filter(Boolean);
    if (lines.length > 0) css.push(`text-decoration:${lines.join(" ")}`);
    if (typeof f.size === "number" && f.size > 0 && f.size !== 11) css.push(`font-size:${Math.round(f.size * (4 / 3))}px`);
    const color = cssColor(f.color);
    if (color && color !== "#000000") css.push(`color:${color}`);
  }
  const fill = style?.fill;
  if (fill?.type === "pattern" && fill.pattern && fill.pattern !== "none") {
    const bg = cssColor(fill.fgColor);
    if (bg) css.push(`background:${bg}`);
  }
  const a = style?.alignment;
  const h = a?.horizontal;
  if (h === "center" || h === "centerContinuous") css.push("text-align:center");
  else if (h === "right") css.push("text-align:right");
  else if (h === "left") css.push("text-align:left");
  else if (numeric) css.push("text-align:right");
  if (a?.vertical === "top") css.push("vertical-align:top");
  else if (a?.vertical === "middle" || a?.vertical === "center") css.push("vertical-align:middle");
  if (a?.wrapText) css.push("white-space:pre-wrap");
  if (typeof a?.indent === "number" && a.indent > 0) css.push(`padding-left:${a.indent * 9 + 3}px`);
  const b = style?.border;
  if (b) {
    for (const side of ["top", "right", "bottom", "left"] as const) {
      const s = b[side];
      if (!s?.style) continue;
      const width = s.style === "thick" ? 3 : s.style === "medium" || s.style.startsWith("medium") ? 2 : 1;
      const kind = s.style.includes("dash") || s.style.includes("Dot") || s.style === "dotted" ? "dashed" : s.style === "double" ? "double" : "solid";
      css.push(`border-${side}:${s.style === "double" ? 3 : width}px ${kind} ${cssColor(s.color) ?? "#000"}`);
    }
  }
  return css.join(";");
}

/** Whether a value is right-aligned by default (numbers and dates). */
export function isNumeric(value: unknown): boolean {
  let v = value;
  if (v !== null && typeof v === "object" && !(v instanceof Date) && "result" in (v as object)) v = (v as { result: unknown }).result;
  return typeof v === "number" || v instanceof Date;
}

/** How many rows and columns of a sheet are drawn. */
export function drawnExtent(rows: number, cols: number): { rows: number; cols: number } {
  const c = Math.min(cols, MAX_COLS);
  return { rows: Math.min(rows, Math.max(1, Math.floor(MAX_CELLS / Math.max(1, c)))), cols: c };
}
