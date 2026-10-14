// The one diff engine (docs/prd/2026-10-14-file-prompts-and-compare.md
// rule 8): an LCS over any sequence (diffSequence), used for the lines of a
// text and for the units of a version (lib/compareUnits.ts). For review
// ("what changed between v3 and v4") the question is which items changed,
// not character-precision merges.

export interface DiffRow {
  type: "same" | "add" | "del";
  text: string;
}

/** One step of a sequence diff: an item kept (in both), added (in b) or
 *  deleted (from a). */
export type SeqStep<T> = { readonly type: "same"; readonly a: T; readonly b: T } | { readonly type: "add"; readonly b: T } | { readonly type: "del"; readonly a: T };

// Beyond this many cells the LCS table would freeze the tab: the diff
// reports a whole replacement instead (review diffs are usually small).
const MAX_CELLS = 4_000_000;

/** Diffs two sequences by LCS under eq; null when they are too long to
 *  align (n × m beyond MAX_CELLS). */
export function diffSequence<T>(a: readonly T[], b: readonly T[], eq: (x: T, y: T) => boolean): SeqStep<T>[] | null {
  const n = a.length;
  const m = b.length;
  if (n * m > MAX_CELLS) return null;
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = eq(a[i], b[j]) ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const out: SeqStep<T>[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (eq(a[i], b[j])) {
      out.push({ type: "same", a: a[i], b: b[j] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ type: "del", a: a[i++] });
    } else {
      out.push({ type: "add", b: b[j++] });
    }
  }
  while (i < n) out.push({ type: "del", a: a[i++] });
  while (j < m) out.push({ type: "add", b: b[j++] });
  return out;
}

/** Diff two texts line-by-line. */
export function diffLines(a: string, b: string): DiffRow[] {
  const A = a.split("\n");
  const B = b.split("\n");
  const steps = diffSequence(A, B, (x, y) => x === y);
  if (!steps) {
    return [
      { type: "del", text: `— ${A.length} lines (previous version)` },
      { type: "add", text: `+ ${B.length} lines (current version)` },
    ];
  }
  return steps.map((s) => (s.type === "add" ? { type: "add", text: s.b } : s.type === "del" ? { type: "del", text: s.a } : { type: "same", text: s.a }));
}
