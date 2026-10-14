// node --test examples/previewers/lib/selection.test.mjs

import assert from "node:assert/strict";
import { test } from "node:test";

import { blockText, collapseText, quotedLabel } from "./selection.ts";

test("quotedLabel names where, then the start of the text", () => {
  assert.equal(quotedLabel("Slide 3", ""), "Slide 3");
  assert.equal(quotedLabel("Page 2", "  Q3 revenue\n grew  "), "Page 2: “Q3 revenue grew”");
  assert.equal(quotedLabel("", "just text"), "“just text”");
  const long = quotedLabel("Page 1", "word ".repeat(40));
  assert.ok(long.endsWith("…”"), long);
  assert.equal([...long.slice("Page 1: “".length, -1)].length, 48);
});

test("collapseText folds whitespace", () => {
  assert.equal(collapseText("\ta\n\n b  "), "a b");
});

test("blockText drops blank lines and joins a lone bullet to its item", () => {
  assert.equal(blockText("Highlights\n• \nRevenue up 18%\n\n  •\nTwo regions\n"), "Highlights\n• Revenue up 18%\n• Two regions");
  assert.equal(blockText("a\tb\nc\td"), "a\tb\nc\td");
});
