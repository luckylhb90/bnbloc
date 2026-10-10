// Pure delimited-text parsing for the table viewer (no DOM; unit-tested
// under node). RFC 4180 quoting: a quoted field may contain the delimiter,
// doubled quotes and line breaks.

export interface ParsedTable {
  readonly rows: readonly (readonly string[])[];
  /** True when parsing stopped at maxRows. */
  readonly truncated: boolean;
  readonly delimiter: "," | "\t" | ";";
}

/** The delimiter of a file: by type (TSV), else the one the first line uses
 *  most among comma, tab and semicolon. */
export function detectDelimiter(text: string, hint?: { mimeType?: string; extension?: string }): "," | "\t" | ";" {
  if (hint?.extension === "tsv" || hint?.mimeType === "text/tab-separated-values") return "\t";
  const first = text.slice(0, text.indexOf("\n") >= 0 ? text.indexOf("\n") : Math.min(text.length, 4096));
  const count = (ch: string) => first.split(ch).length - 1;
  const tabs = count("\t");
  const commas = count(",");
  const semis = count(";");
  if (tabs > commas && tabs >= semis) return "\t";
  if (semis > commas) return ";";
  return ",";
}

export function parseDelimited(text: string, delimiter: "," | "\t" | ";", maxRows = Infinity): ParsedTable {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let quoted = false;
  const n = text.length;
  // Skip a UTF-8 byte-order mark.
  let i = text.charCodeAt(0) === 0xfeff ? 1 : 0;
  const endRow = () => {
    row.push(cell);
    cell = "";
    // A blank line is not a row.
    if (!(row.length === 1 && row[0] === "")) rows.push(row);
    row = [];
  };
  while (i < n) {
    const ch = text[i];
    if (quoted) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          cell += '"';
          i += 2;
          continue;
        }
        quoted = false;
        i += 1;
        continue;
      }
      cell += ch;
      i += 1;
      continue;
    }
    if (ch === '"' && cell === "") {
      quoted = true;
      i += 1;
      continue;
    }
    if (ch === delimiter) {
      row.push(cell);
      cell = "";
      i += 1;
      continue;
    }
    if (ch === "\n" || ch === "\r") {
      endRow();
      if (ch === "\r" && text[i + 1] === "\n") i += 1;
      i += 1;
      if (rows.length >= maxRows) return { rows, truncated: i < n, delimiter };
      continue;
    }
    cell += ch;
    i += 1;
  }
  if (cell !== "" || row.length > 0) endRow();
  return { rows, truncated: false, delimiter };
}

/** Column widths in px from a sample of rows (character counts, clamped). */
export function columnWidths(rows: readonly (readonly string[])[], sample = 200, charPx = 7.5): number[] {
  const widths: number[] = [];
  for (const row of rows.slice(0, sample)) {
    row.forEach((cell, c) => {
      widths[c] = Math.max(widths[c] ?? 0, Math.min(cell.length, 60));
    });
  }
  return widths.map((chars) => Math.round(Math.min(360, Math.max(64, chars * charPx + 20))));
}
