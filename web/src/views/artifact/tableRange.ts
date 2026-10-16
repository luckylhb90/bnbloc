// A selected range of a delimited table, as the Ask AI panel names it and as
// Compare's units address it (compare/tableUnits.ts: "B3", the header being
// row 1). Pure, unit-tested under node.

import { columnName } from "./compare/tableUnits.ts";

/** Zero-based file rows and columns, both ends inclusive (r0 <= r1, c0 <= c1). */
export interface CellRange {
  readonly r0: number;
  readonly c0: number;
  readonly r1: number;
  readonly c1: number;
}

export const MAX_RANGE_KEYS = 500;
const DETAIL_ROWS = 40;
const DETAIL_COLS = 20;

/** The range spanned by two cells, either way round. */
export function spanCells(a: { r: number; c: number }, b: { r: number; c: number }): CellRange {
  return { r0: Math.min(a.r, b.r), c0: Math.min(a.c, b.c), r1: Math.max(a.r, b.r), c1: Math.max(a.c, b.c) };
}

const address = (r: number, c: number): string => `${columnName(c + 1)}${r + 1}`;

/** Whether the range is whole columns (a header click). */
export function isWholeColumns(range: CellRange, rowCount: number): boolean {
  return range.r0 === 0 && range.r1 >= rowCount - 1 && rowCount > 1;
}

/** "B3", "B2:C5", or "Column B" / "Columns B:C" for whole columns. */
export function rangeLabel(range: CellRange, rowCount: number): string {
  const a = columnName(range.c0 + 1);
  const b = columnName(range.c1 + 1);
  if (isWholeColumns(range, rowCount)) return range.c0 === range.c1 ? `Column ${a}` : `Columns ${a}:${b}`;
  const from = address(range.r0, range.c0);
  const to = address(range.r1, range.c1);
  return from === to ? from : `${from}:${to}`;
}

/** Every address in the range (empty cells included: an edit that fills one
 *  is inside it), or undefined when there are more than MAX_RANGE_KEYS (a
 *  selection then names no units and falls back to text). */
export function rangeKeys(range: CellRange): string[] | undefined {
  const count = (range.r1 - range.r0 + 1) * (range.c1 - range.c0 + 1);
  if (count > MAX_RANGE_KEYS) return undefined;
  const out: string[] = [];
  for (let r = range.r0; r <= range.r1; r++) for (let c = range.c0; c <= range.c1; c++) out.push(address(r, c));
  return out;
}

/** What the agent reads: the columns' names when row 1 is a header, then the
 *  range's values as tab-separated rows (at most DETAIL_ROWS × DETAIL_COLS,
 *  the rest said). */
export function rangeDetail(rows: readonly (readonly string[])[], range: CellRange, headerRow: boolean): string {
  const lines: string[] = [];
  const cols = Math.min(range.c1 - range.c0 + 1, DETAIL_COLS);
  if (headerRow && rows[0]) {
    const names = Array.from({ length: cols }, (_, i) => `${columnName(range.c0 + i + 1)} = ${rows[0][range.c0 + i] ?? ""}`);
    lines.push(`Columns: ${names.join(", ")}`);
  }
  const last = Math.min(range.r1, range.r0 + DETAIL_ROWS - 1);
  for (let r = range.r0; r <= last; r++) {
    lines.push(Array.from({ length: cols }, (_, i) => rows[r]?.[range.c0 + i] ?? "").join("\t"));
  }
  if (range.r1 > last) lines.push(`… and ${range.r1 - last} more rows`);
  if (range.c1 - range.c0 + 1 > cols) lines.push(`… and ${range.c1 - range.c0 + 1 - cols} more columns`);
  return lines.join("\n");
}
