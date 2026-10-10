import { DEFAULT_NOTIFICATION_PREFS, describeTestResult, isRoomMuted, withRoomMuted } from "./notificationPrefs.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
const assert = {
  equal(actual: unknown, expected: unknown, message = ""): void {
    if (actual !== expected) throw new Error(`${message} expected ${String(expected)}, got ${String(actual)}`);
  },
  deepEqual(actual: unknown, expected: unknown): void {
    if (JSON.stringify(actual) !== JSON.stringify(expected)) {
      throw new Error(`expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
    }
  },
};

test("muting adds the room once and keeps the other settings", () => {
  const base = { ...DEFAULT_NOTIFICATION_PREFS, mentions: false, quietRooms: ["a"] };
  const muted = withRoomMuted(base, "b", true);
  assert.deepEqual(muted.quietRooms, ["a", "b"]);
  assert.equal(muted.mentions, false);
  assert.deepEqual(withRoomMuted(muted, "b", true).quietRooms, ["a", "b"]);
  assert.equal(isRoomMuted(muted, "b"), true);
});

test("unmuting removes only that room", () => {
  const prefs = { ...DEFAULT_NOTIFICATION_PREFS, quietRooms: ["a", "b"] };
  assert.deepEqual(withRoomMuted(prefs, "a", false).quietRooms, ["b"]);
  assert.equal(isRoomMuted(withRoomMuted(prefs, "a", false), "a"), false);
  assert.equal(isRoomMuted(null, "a"), false);
});

test("the default prefs are not mutated", () => {
  withRoomMuted(DEFAULT_NOTIFICATION_PREFS, "x", true);
  assert.deepEqual(DEFAULT_NOTIFICATION_PREFS.quietRooms, []);
});

test("test-send outcomes read as toasts", () => {
  assert.deepEqual(describeTestResult({ delivered: true, status: 204, eventId: "e" }), {
    ok: true,
    message: "Test notification delivered (HTTP 204)",
  });
  assert.deepEqual(describeTestResult({ delivered: false, error: "notifier answered HTTP 401", eventId: "e" }), {
    ok: false,
    message: "Test notification failed: notifier answered HTTP 401",
  });
});
