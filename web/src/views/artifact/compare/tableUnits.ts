// A delimited table's units for Compare: one per non-empty cell, keyed by
// its address ("B3"), labelled with its column header when the first row
// reads as one ("B3 · Q2"). Pure, unit-tested under node.

import type { CompareUnit } from "../../../../previewer-sdk/src/compare.ts";
import { detectDelimiter, parseDelimited } from "../tableData.ts";

/** 1 → "A", 27 → "AA". */
export function columnName(n: number): string {
  let s = "";
  for (let i = n; i > 0; i = Math.floor((i - 1) / 26)) s = String.fromCharCode(65 + ((i - 1) % 26)) + s;
  return s;
}

export function tableUnits(text: string, hint: { mimeType?: string; extension?: string }, maxCells: number): { units: CompareUnit[]; truncated: boolean } {
  const { rows } = parseDelimited(text, detectDelimiter(text, hint));
  const header = rows[0] ?? [];
  const units: CompareUnit[] = [];
  for (let r = 0; r < rows.length; r++) {
    for (let c = 0; c < rows[r].length; c++) {
      const value = rows[r][c];
      if (value === "") continue;
      if (units.length >= maxCells) return { units, truncated: true };
      const address = `${columnName(c + 1)}${r + 1}`;
      const head = r > 0 ? header[c]?.trim() : "";
      units.push({ key: address, label: head ? `${address} · ${head}` : address, text: value });
    }
  }
  return { units, truncated: false };
}
