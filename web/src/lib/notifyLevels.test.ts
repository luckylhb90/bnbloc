import assert from "node:assert/strict";
import test from "node:test";
import { notifyCaption, notifyChoices, notifySaved } from "./notifyLevels.ts";

test("the default is named by what it means now", () => {
  assert.deepEqual(notifyChoices("all")[0], { value: "", short: "Default", label: "Default (all messages)" });
  assert.equal(notifyChoices(undefined)[0].label, "Default (mentions only)");
  assert.deepEqual(notifyChoices("mentions").map((c) => c.value), ["", "all", "mentions", "muted"]);
  assert.equal(notifyCaption("", "all"), "Default here: all messages");
  assert.equal(notifyCaption("muted", "muted"), "Muted: only a direct mention");
});

test("a mute says a direct mention still comes through", () => {
  assert.match(notifySaved("design", "muted"), /direct mention/);
});
