import assert from "node:assert/strict";
import { test } from "node:test";
import { placeSurface } from "./menuPlacement.ts";

const view = { width: 1000, height: 800 };

test("a menu aligned right under a trigger in a narrow rail opens below, inside the window", () => {
  // The rail's kebab: near the left edge, a 224px menu aligned to its right edge.
  const p = placeSurface({ left: 150, right: 178, top: 100, bottom: 128 }, { width: 224, height: 300 }, view, { above: false, alignRight: true });
  assert.equal(p.top, 132);
  assert.ok(p.left >= 8, "never past the window's left edge");
  assert.equal(p.left, 8);
});

test("a menu with no room below flips above", () => {
  const p = placeSurface({ left: 400, right: 430, top: 700, bottom: 728 }, { width: 200, height: 300 }, view, { above: false, alignRight: false });
  assert.equal(p.top, 700 - 4 - 300);
});

test("a menu that prefers above opens below when above has less room", () => {
  const p = placeSurface({ left: 400, right: 430, top: 40, bottom: 68 }, { width: 200, height: 300 }, view, { above: true, alignRight: false });
  assert.equal(p.top, 72);
});

test("a menu wider than the window is narrowed to it", () => {
  const p = placeSurface({ left: 10, right: 40, top: 100, bottom: 128 }, { width: 400, height: 100 }, { width: 300, height: 800 }, { above: false, alignRight: false });
  assert.equal(p.maxWidth, 284);
  assert.equal(p.left, 8);
});
