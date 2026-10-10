// Line-level diff for artifact version comparison.
// Simple LCS over lines: for spec/code review "what changed between v3 and v4"
// is the question, not character-precision merges.

export interface DiffRow {
  type: "same" | "add" | "del";
  text: string;
}

/** Diff two texts line-by-line (LCS). */
export function diffLines(a: string, b: string): DiffRow[] {
  const A = a.split("\n");
  const B = b.split("\n");
  const n = A.length;
  const m = B.length;
  // LCS table. Artifacts are capped at 2 MiB, but review diffs are between
  // adjacent versions and typically small; guard pathological inputs so the
  // panel never freezes the tab.
  if (n * m > 4_000_000) {
    return [
      { type: "del", text: `— ${n} lines (previous version)` },
      { type: "add", text: `+ ${m} lines (current version)` },
    ];
  }
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = A[i] === B[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const rows: DiffRow[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (A[i] === B[j]) {
      rows.push({ type: "same", text: A[i] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      rows.push({ type: "del", text: A[i] });
      i++;
    } else {
      rows.push({ type: "add", text: B[j] });
      j++;
    }
  }
  while (i < n) rows.push({ type: "del", text: A[i++] });
  while (j < m) rows.push({ type: "add", text: B[j++] });
  return rows;
}
