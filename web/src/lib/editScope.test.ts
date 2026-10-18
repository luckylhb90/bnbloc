import { outsideSelection, overlaps } from "./editScope.ts";
import type { UnitChange } from "../../previewer-sdk/src/compare.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const u = (key: string | undefined, text: string) => ({ ...(key ? { key } : {}), label: key ?? text, text });
const changed = (key: string | undefined, a: string, b: string): UnitChange => ({ kind: "changed", prev: u(key, a), curr: u(key, b) });
const labels = (c: UnitChange[]) => c.map((x) => (x.kind === "removed" ? x.prev.label : x.curr.label));

// Keys: a slide selected, another one also changed.
{
  const changes: UnitChange[] = [changed("slide-257", "Highlights", "Key highlights"), changed("slide-259", "Growth", "Growth 2026"), { kind: "same", prev: u("slide-256", "x"), curr: u("slide-256", "x") }];
  eq(labels(outsideSelection(changes, { keys: ["slide-257"] })), ["slide-259"], "a slide outside");
  eq(labels(outsideSelection([{ kind: "added", curr: u("slide-300", "New") }], { keys: ["slide-257"] })), ["slide-300"], "an added slide is outside");
  // A range sends every cell address: filling an empty cell inside is not outside.
  eq(outsideSelection([{ kind: "added", curr: u("Revenue!C4", "10") }], { keys: ["Revenue!B3", "Revenue!C4"] }), [], "a filled cell in the range");
}
// Text: a paragraph that does not overlap the selection is outside; an
// added paragraph (an expansion) is not.
{
  const sel = { detail: "Revenue grew 18% year over year, led by two new regions." };
  const changes: UnitChange[] = [
    changed(undefined, "Revenue grew 18% year over year, led by two new regions.", "Revenue grew 18% year over year."),
    changed(undefined, "Churn down to 2.1%", "Churn fell to 2.1%"),
    { kind: "added", curr: u(undefined, "A new point") },
  ];
  eq(labels(outsideSelection(changes, sel)), ["Churn fell to 2.1%"], "text overlap");
  eq(outsideSelection(changes, {}), [], "no selection, nothing outside");
}
eq([overlaps("Hello   World", "hello world"), overlaps("abc", "xyz"), overlaps("the quick brown fox jumps over the lazy dog today", "a lazy start: the quick brown fox jumps over it")], [true, false, true], "overlaps");
