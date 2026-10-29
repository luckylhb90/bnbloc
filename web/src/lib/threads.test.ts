import assert from "node:assert/strict";
import test from "node:test";
import { closingSummary, followedOrder, mergeSummary, replyRefOf, rootLine, summaryOf, threadAssistText, threadsByRoot } from "./threads.ts";
import type { RoomChannel } from "./wire.gen.ts";

const thread = (id: string, parentId: string, rootSeq: number, over: Partial<RoomChannel> = {}, last = 0): RoomChannel => ({
  channelId: id, name: id, isDefault: false, kind: "thread", createdBy: "maya", purpose: "", rules: "", sectionId: "", position: 0,
  ownerId: "maya", settings: {}, joined: true, starred: false, hidden: false, groupId: "",
  thread: { parentId, rootSeq, replies: 2, lastReplyAtMs: last, participants: ["maya", "jack"] }, ...over,
});

test("a root's line says the replies, who wrote and whether it is closed", () => {
  const s = summaryOf(thread("t1", "ch", 5))!;
  assert.equal(rootLine(s), "2 replies · maya, jack");
  assert.equal(rootLine({ ...s, archived: true, info: { ...s.info, replies: 1, participants: ["a", "b", "c", "d", "e"] } }), "1 reply · a, b, c +2 · closed");
  assert.equal(rootLine({ ...s, info: { ...s.info, replies: 0, participants: [] } }), "No replies yet");
  assert.equal(summaryOf({ ...thread("x", "ch", 1), thread: undefined }), null);
});

test("threads hang under their root in their parent; a newer summary replaces the older", () => {
  const a = summaryOf(thread("t1", "ch", 5))!;
  const b = summaryOf(thread("t2", "other", 5))!;
  const byRoot = threadsByRoot("ch", [a, b]);
  assert.equal(byRoot.size, 1);
  assert.equal(byRoot.get(5)?.threadId, "t1");
  const merged = mergeSummary([a, b], { ...a, info: { ...a.info, replies: 3 } });
  assert.equal(merged.length, 2);
  assert.equal(merged[0].info.replies, 3);
});

test("followed threads list unread first, then newest activity", () => {
  const list = [thread("old", "c", 1, {}, 10), thread("new", "c", 2, {}, 30), thread("quiet", "c", 3, {}, 20), thread("notmine", "c", 4, { joined: false }, 99)];
  assert.deepEqual(followedOrder(list, { quiet: 2 }).map((c) => c.channelId), ["quiet", "new", "old"]);
});

test("a reply's reference reads only a well-formed payload", () => {
  assert.deepEqual(replyRefOf({ seq: 4, author: "maya", excerpt: "hi" }), { seq: 4, author: "maya", excerpt: "hi" });
  assert.equal(replyRefOf({ seq: 0 }), undefined);
  assert.equal(replyRefOf("x"), undefined);
});

test("the thread assists get bounded text; the closing summary reads decided and open", () => {
  const text = threadAssistText("Bug", [{ author: "maya", text: "it  crashes\non login" }], 100);
  assert.equal(text, "Title: Bug\nmaya: it crashes on login\n");
  assert.ok(threadAssistText("T", Array.from({ length: 50 }, () => ({ author: "a", text: "x".repeat(40) })), 200).length <= 200);
  assert.equal(closingSummary({ summary: "Fixed.", decided: ["Ship Friday"], open: [] }), "Fixed.\n\nDecided:\n- Ship Friday");
  assert.equal(closingSummary({ decided: "x", open: ["Who tests?"] }), "Open:\n- Who tests?");
});
