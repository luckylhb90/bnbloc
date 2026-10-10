import { isRoomGoneStatus, onRoomGone, reportRoomGone, roomAccessEnded, withoutRoom } from "./roomGone.ts";

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

test("only 404 and 403 mean the room is gone", () => {
  assert.equal(isRoomGoneStatus(404), true);
  assert.equal(isRoomGoneStatus(403), true);
  for (const transient of [0, 401, 429, 500, 502, 503]) assert.equal(isRoomGoneStatus(transient), false, String(transient));
});

test("room_deleted and the member's own removal end access; others do not", () => {
  assert.equal(roomAccessEnded({ kind: "room_deleted" }, "alice"), "deleted");
  assert.equal(roomAccessEnded({ kind: "member_removed", removedUser: "Alice" }, "alice"), "removed");
  assert.equal(roomAccessEnded({ kind: "member_removed", removedUser: "bob" }, "alice"), null);
  assert.equal(roomAccessEnded({ kind: "member_removed", removedUser: "alice" }, null), null);
  assert.equal(roomAccessEnded({ kind: "chat_message" }, "alice"), null);
});

test("withoutRoom prunes the id and keeps identity when absent", () => {
  const rooms = [{ roomId: "a" }, { roomId: "gone" }, { roomId: "b" }];
  assert.deepEqual(
    withoutRoom(rooms, "gone").map((r) => r.roomId),
    ["a", "b"],
  );
  // Unchanged list -> same array, so a state setter does not re-render and
  // the unread poll (keyed on the id set) does not restart.
  assert.equal(withoutRoom(rooms, "missing"), rooms);
});

test("a reported gone room reaches every subscriber until it unsubscribes", () => {
  const seenA: string[] = [];
  const seenB: string[] = [];
  const offA = onRoomGone((id) => seenA.push(id));
  const offB = onRoomGone((id) => seenB.push(id));
  reportRoomGone("r1");
  offA();
  reportRoomGone("r2");
  offB();
  reportRoomGone("r3");
  assert.deepEqual(seenA, ["r1"]);
  assert.deepEqual(seenB, ["r1", "r2"]);
});

test("the sidebar flow: a gone room leaves the list and the unread poll's id set", () => {
  let rooms = [{ roomId: "keep" }, { roomId: "deleted" }];
  const off = onRoomGone((id) => {
    rooms = withoutRoom(rooms, id);
  });
  reportRoomGone("deleted");
  off();
  const pollIds = rooms.map((r) => r.roomId);
  assert.deepEqual(pollIds, ["keep"]);
});
