import { blockText, collapseText, pendingReveal, quotedLabel } from "./pointing.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq(quotedLabel("Slide 3", ""), "Slide 3", "no text: where only");
eq(quotedLabel("Page 2", "  Q3 revenue\n grew  "), "Page 2: “Q3 revenue grew”", "text folded and quoted");
eq(quotedLabel("", "just text"), "“just text”", "no where");
const long = quotedLabel("Page 1", "word ".repeat(40));
eq(long.endsWith("…”"), true, "a long quote is cut");
eq([...long.slice("Page 1: “".length, -1)].length, 48, "at 48 characters");
eq(collapseText("\ta\n\n b  "), "a b", "whitespace folds");
eq(blockText("Highlights\n• \nRevenue up 18%\n\n  •\nTwo regions\n"), "Highlights\n• Revenue up 18%\n• Two regions", "a lone bullet joins its item");
eq(blockText("a\tb\nc\td"), "a\tb\nc\td", "tabs stay (tables)");

// A reveal waits until the viewer can place it.
{
  let ready = false;
  const shown: string[] = [];
  const r = pendingReveal((at) => {
    if (!ready) return false;
    shown.push(at);
    return true;
  });
  r.ask("cell:a/b");
  r.apply();
  eq(shown, [], "not shown before the file is");
  ready = true;
  r.apply();
  r.apply();
  eq(shown, ["cell:a/b"], "shown once when it can be");
  r.ask("page:2");
  eq(shown, ["cell:a/b", "page:2"], "shown at once when ready");
}
