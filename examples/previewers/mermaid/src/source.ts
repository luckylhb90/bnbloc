// What the Mermaid previewer reads of a diagram's source, as pure functions
// (no DOM): the lines that name a part a member picked, the id of a node
// from the element mermaid drew it as, and the source's statements as
// Compare units. docs/prd/2026-10-16-diagram-previewers.md rules 4 and 6.

import type { UnitDraft } from "../../../../web/previewer-sdk/src/index.ts";

const LINE_QUOTE_LIMIT = 160;
const MAX_NAMING_LINES = 3;

function flat(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

/** The source lines that contain `text` (the picked part's label), as
 *  "line 12: B -->|yes| C[Ship it]", at most three: what the agent edits.
 *  A label drawn on several lines (mermaid breaks long ones) is searched as
 *  its words in order. */
export function linesNaming(source: string, text: string, limit = MAX_NAMING_LINES): string[] {
  const needle = flat(text);
  if (!needle) return [];
  const words = needle.split(" ");
  const out: string[] = [];
  const lines = source.split(/\r?\n/);
  for (let i = 0; i < lines.length && out.length < limit; i++) {
    const line = flat(lines[i]);
    if (!line || line.startsWith("%%")) continue;
    if (line.includes(needle) || includesInOrder(line, words)) {
      const quoted = [...line].length > LINE_QUOTE_LIMIT ? `${[...line].slice(0, LINE_QUOTE_LIMIT - 1).join("")}…` : line;
      out.push(`line ${i + 1}: ${quoted}`);
    }
  }
  return out;
}

function includesInOrder(line: string, words: readonly string[]): boolean {
  if (words.length < 2) return false;
  let at = 0;
  for (const w of words) {
    const found = line.indexOf(w, at);
    if (found < 0) return false;
    at = found + w.length;
  }
  return true;
}

/** The node id a mermaid element id carries. Mermaid names a part
 *  "<render id>-<diagram type>-<node id>-<index>" ("bnbloc-mermaid-1-
 *  flowchart-B-1" → "B"); a part without one (an arrow's label) or the
 *  drawing itself has none: "". */
export function nodeIdOf(elementId: string, renderId: string): string {
  if (!elementId.startsWith(`${renderId}-`)) return "";
  return /^[a-z]+-(.+?)(?:-\d+)?$/i.exec(elementId.slice(renderId.length + 1))?.[1] ?? "";
}

/** The source's statements as Compare units: blank lines and `%%` comments
 *  dropped, each statement one unit labelled with its line, unkeyed (the
 *  diff aligns them by text, so inserting a line shifts nothing). */
export function sourceUnits(source: string): UnitDraft[] {
  const units: UnitDraft[] = [];
  const lines = source.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const text = lines[i].trim();
    if (!text || text.startsWith("%%")) continue;
    units.push({ label: `Line ${i + 1}`, text });
  }
  return units;
}

/** A parse error as one short message: the first lines of mermaid's report
 *  (it names the line and shows the offending source with a caret). */
export function parseErrorMessage(err: unknown): string {
  const raw = err instanceof Error ? err.message : typeof err === "string" ? err : String(err);
  const text = raw.replace(/\r/g, "").trim();
  return text.length > 400 ? `${text.slice(0, 399)}…` : text;
}
