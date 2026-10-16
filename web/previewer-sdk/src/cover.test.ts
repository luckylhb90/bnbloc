import { coverLines } from "./cover.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq(coverLines("\n\n  a\r\nb\t\tc  \n"), ["  a", "b        c"], "endings and tabs normalized, leading blanks and trailing spaces gone");
eq(coverLines("a\n\n\n\nb"), ["a", "", "b"], "a run of blank lines folds to one");
eq(coverLines("a\nb\nc\nd", 2), ["a", "b"], "at most maxLines");
eq(coverLines("x".repeat(100), 5, 10), ["xxxxxxxxx…"], "a long line is cut with an ellipsis");
eq(coverLines("x".repeat(10), 5, 10), ["xxxxxxxxxx"], "a line that fits is not cut");
eq(coverLines("x".repeat(11), 5, 10), ["xxxxxxxxx…"], "one column over is");
eq(coverLines("短".repeat(10), 5, 11), ["短短短短短…"], "a full-width character counts for two columns");
eq(coverLines(""), [], "no text, no lines");
eq(coverLines("\n \n\t\n"), [], "only blanks, no lines");
