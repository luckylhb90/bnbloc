import assert from "node:assert/strict";
import test from "node:test";
import { notifyChoices, notifySaved } from "./notifyLevels.ts";

test("the default is named by what it means now", () => {
  assert.deepEqual(notifyChoices("all")[0], { level: "", label: "Default (all messages)" });
  assert.equal(notifyChoices(undefined)[0].label, "Default (mentions only)");
  assert.deepEqual(notifyChoices("mentions").map((c) => c.level), ["", "all", "mentions", "muted"]);
});

test("a mute says a direct mention still comes through", () => {
  assert.match(notifySaved("design", "muted"), /direct mention/);
});
