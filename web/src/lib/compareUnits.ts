// Comparing two versions by their units (docs/prd/2026-10-14-file-prompts-and-compare.md
// rules 6–8). A comparer splits a version into ordered units: a slide, a
// paragraph, a cell, a page, or the whole text as one. diffUnits aligns the
// two lists and says, per unit, whether it is the same, changed, added or
// removed; the view shows the changes and collapses the rest. Pure,
// unit-tested under node.

import type { Unit } from "../../previewer-sdk/src/protocol.ts";
import { diffSequence, type SeqStep } from "./diff.ts";

/** A unit of a version: the frame protocol's Unit (one shape, whether a
 *  previewer or a built-in comparer made it). */
export type CompareUnit = Unit;

export type UnitChange =
  | { readonly kind: "same"; readonly prev: CompareUnit; readonly curr: CompareUnit }
  | { readonly kind: "changed"; readonly prev: CompareUnit; readonly curr: CompareUnit }
  | { readonly kind: "added"; readonly curr: CompareUnit }
  | { readonly kind: "removed"; readonly prev: CompareUnit };

function keyed(units: readonly CompareUnit[]): boolean {
  const seen = new Set<string>();
  for (const u of units) {
    if (!u.key || seen.has(u.key)) return false;
    seen.add(u.key);
  }
  return true;
}

/** Aligns two versions' units. Keyed lists (every unit keyed, keys unique,
 *  in both) align by key, so a slide edited in place is "changed" wherever
 *  it moved. Unkeyed lists align by text; a run of removed units next to a
 *  run of added ones pairs up, in order, as changes (an edited paragraph is
 *  one change, not a removal and an addition). Lists too long to align are
 *  one replacement. */
export function diffUnits(prev: readonly CompareUnit[], curr: readonly CompareUnit[]): UnitChange[] {
  if (keyed(prev) && keyed(curr) && prev.length + curr.length > 0) {
    const steps = diffSequence(prev, curr, (a, b) => a.key === b.key);
    if (steps) {
      return steps.map((s) =>
        s.type === "same" ? { kind: s.a.text === s.b.text ? "same" : "changed", prev: s.a, curr: s.b } : s.type === "add" ? { kind: "added", curr: s.b } : { kind: "removed", prev: s.a },
      );
    }
  }
  const steps = diffSequence(prev, curr, (a, b) => a.text === b.text);
  if (!steps) return [...prev.map((u): UnitChange => ({ kind: "removed", prev: u })), ...curr.map((u): UnitChange => ({ kind: "added", curr: u }))];
  return pairRuns(steps);
}

function pairRuns(steps: readonly SeqStep<CompareUnit>[]): UnitChange[] {
  const out: UnitChange[] = [];
  let dels: CompareUnit[] = [];
  let adds: CompareUnit[] = [];
  const flush = () => {
    const n = Math.min(dels.length, adds.length);
    for (let i = 0; i < n; i++) out.push({ kind: "changed", prev: dels[i], curr: adds[i] });
    for (const u of dels.slice(n)) out.push({ kind: "removed", prev: u });
    for (const u of adds.slice(n)) out.push({ kind: "added", curr: u });
    dels = [];
    adds = [];
  };
  for (const s of steps) {
    if (s.type === "del") dels.push(s.a);
    else if (s.type === "add") adds.push(s.b);
    else {
      flush();
      out.push({ kind: "same", prev: s.a, curr: s.b });
    }
  }
  flush();
  return out;
}

/** How many units each kind of change counts. */
export function changeCounts(changes: readonly UnitChange[]): Record<UnitChange["kind"], number> {
  const out = { same: 0, changed: 0, added: 0, removed: 0 };
  for (const c of changes) out[c.kind] += 1;
  return out;
}
