import assert from "node:assert/strict";
import test from "node:test";
import { boardColumns, forumFiltered, forumOrder, isForum, linesOf, statusesFromLines, tagsFromLines, vocabularyId } from "./forum.ts";
import type { RoomChannel } from "./wire.gen.ts";

const th = (id: string, rootSeq: number, over: Partial<NonNullable<RoomChannel["thread"]>> = {}, archived = false): RoomChannel => ({
  channelId: id, name: id, isDefault: false, kind: "thread", createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 0,
  ownerId: "maya", settings: {}, joined: true, starred: false, hidden: false, groupId: "",
  ...(archived ? { archivedAtMs: 5 } : {}),
  thread: { parentId: "ch", rootSeq, replies: 0, participants: [], ...over },
});

test("a forum is the threads posting style", () => {
  assert.equal(isForum({ postingStyle: "threads" }), true);
  assert.equal(isForum({}), false);
  assert.equal(isForum(undefined), false);
});

test("tags and statuses are one name a line; ids come from names and stay", () => {
  assert.equal(vocabularyId("  In Progress! "), "in-progress");
  assert.ok(vocabularyId("缺陷").length > 0 && /^[a-z0-9-]+$/.test(vocabularyId("缺陷")), "a name of no latin letters still has an id");
  assert.deepEqual(tagsFromLines("Bug\n\nFeature\nbug\nBug"), [{ id: "bug", name: "Bug" }, { id: "feature", name: "Feature" }]);
  // An existing tag of the same name keeps its id (and its color).
  assert.deepEqual(tagsFromLines("Bug", [{ id: "b1", name: "Bug", color: "red" }]), [{ id: "b1", name: "Bug", color: "red" }]);
  assert.equal(statusesFromLines(Array.from({ length: 20 }, (_, i) => `s${i}`).join("\n")).length, 12, "at most 12 statuses");
  assert.equal(linesOf([{ name: "Open" }, { name: "Done" }]), "Open\nDone");
});

test("a forum lists open threads first, newest activity first", () => {
  const list = forumOrder([th("old", 1, { lastReplyAtMs: 10 }), th("closed", 9, { lastReplyAtMs: 99 }, true), th("new", 2, { lastReplyAtMs: 50 }), th("quiet", 7)]);
  assert.deepEqual(list.map((t) => t.channelId), ["new", "old", "quiet", "closed"]);
});

test("filters by tag and status; the board has a column per status, then no status", () => {
  const ts = [th("a", 1, { tags: ["bug"], status: "open" }), th("b", 2, { tags: ["ui"], status: "done" }), th("c", 3, { status: "gone" })];
  assert.deepEqual(forumFiltered(ts, { tag: "bug" }).map((t) => t.channelId), ["a"]);
  assert.deepEqual(forumFiltered(ts, { status: "done" }).map((t) => t.channelId), ["b"]);
  assert.equal(forumFiltered(ts, {}).length, 3);
  const cols = boardColumns([{ id: "open", name: "Open" }, { id: "done", name: "Done" }], ts);
  assert.deepEqual(cols.map((c) => [c.id, c.threads.map((t) => t.channelId)]), [["", ["c"]], ["open", ["a"]], ["done", ["b"]]]);
  // No "No status" column when every thread has a defined status.
  assert.deepEqual(boardColumns([{ id: "open", name: "Open" }], [ts[0]]).map((c) => c.id), ["open"]);
});
