// Did an edit stay inside what the member selected?
// (docs/prd/2026-10-14-select-edit-review.md rule 5). Given the changes of
// an edit and the request's selection, the changes outside it:
//   - with the selection's unit keys (a slide, every cell of a range): any
//     changed, added or removed unit whose key is not among them;
//   - without (paragraphs, pages): a changed or removed unit whose text does
//     not overlap the selection's text. Added units are left out there:
//     inserting next to the selection is how "expand this" works.
// Pure, unit-tested under node.

import type { UnitChange } from "./compareUnits.ts";

export interface EditSelection {
  readonly detail?: string;
  readonly keys?: readonly string[];
}

const WINDOW = 24;

function norm(s: string): string {
  return s.replace(/\s+/g, " ").trim().toLowerCase();
}

/** Whether two texts share a passage: one holds the other, or they share a
 *  run of WINDOW characters. */
export function overlaps(a: string, b: string): boolean {
  const x = norm(a);
  const y = norm(b);
  if (!x || !y) return false;
  const [short, long] = x.length <= y.length ? [x, y] : [y, x];
  if (long.includes(short)) return true;
  if (short.length < WINDOW) return false;
  // A step of 4 finds any shared run of WINDOW + 3 characters.
  for (let i = 0; i + WINDOW <= short.length; i += 4) {
    if (long.includes(short.slice(i, i + WINDOW))) return true;
  }
  return false;
}

export function outsideSelection(changes: readonly UnitChange[], sel: EditSelection): UnitChange[] {
  if (sel.keys?.length) {
    const inside = new Set(sel.keys);
    return changes.filter((c) => {
      if (c.kind === "same") return false;
      const unit = c.kind === "removed" ? c.prev : c.curr;
      return !unit.key || !inside.has(unit.key);
    });
  }
  const detail = sel.detail ?? "";
  if (!norm(detail)) return [];
  return changes.filter((c) => (c.kind === "changed" || c.kind === "removed") && !overlaps(c.prev.text, detail));
}
