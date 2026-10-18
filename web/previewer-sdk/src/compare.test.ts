import { changeCounts, diffUnits, type CompareUnit } from "./compare.ts";
import { diffLines, diffSequence } from "./diff.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const p = (text: string): CompareUnit => ({ label: "", text });
const slide = (key: string, text: string): CompareUnit => ({ key, label: `Slide ${key}`, text });
const kinds = (c: ReturnType<typeof diffUnits>) => c.map((x) => x.kind);

// An inserted paragraph in the middle is one addition, not "everything
// changed" (the PRD's first pre-mortem).
{
  const prev = ["Intro", "Body one", "Body two", "Close"].map(p);
  const curr = ["Intro", "Body one", "New point", "Body two", "Close"].map(p);
  eq(kinds(diffUnits(prev, curr)), ["same", "same", "added", "same", "same"], "insertion");
}
// An edited paragraph is one change; a removal next to more additions
// pairs in order and keeps the rest added.
{
  const prev = ["Intro", "Old body", "Close"].map(p);
  const curr = ["Intro", "New body", "Extra", "Close"].map(p);
  const got = diffUnits(prev, curr);
  eq(kinds(got), ["same", "changed", "added", "same"], "edit pairs");
  eq(got[1].kind === "changed" && [got[1].prev.text, got[1].curr.text], ["Old body", "New body"], "the pair");
}
// Keyed units align by key: a slide moved and edited is changed, not
// removed and added; a new slide is added; a deleted one removed.
{
  const prev = [slide("a", "Title"), slide("b", "Highlights"), slide("c", "Thanks")];
  const curr = [slide("a", "Title"), slide("b", "Key highlights"), slide("d", "Appendix"), slide("c", "Thanks")];
  eq(kinds(diffUnits(prev, curr)), ["same", "changed", "added", "same"], "keyed");
  eq(changeCounts(diffUnits(prev, [slide("a", "Title")])), { same: 1, changed: 0, added: 0, removed: 2 }, "removed");
}
// Duplicate or missing keys fall back to text alignment.
eq(kinds(diffUnits([slide("a", "x"), slide("a", "y")], [slide("a", "x")])), ["same", "removed"], "duplicate keys");
// A whole text as one unit: same, or one change.
eq(kinds(diffUnits([p("a\nb")], [p("a\nb")])), ["same"], "same text");
eq(kinds(diffUnits([p("a\nb")], [p("a\nc")])), ["changed"], "changed text");
// The line diff is the same engine.
eq(diffLines("a\nb\nc", "a\nc\nd").map((r) => r.type), ["same", "del", "same", "add"], "lines");
eq(diffSequence(new Array(3000).fill(1), new Array(3000).fill(2), (x, y) => x === y), null, "too long to align");
