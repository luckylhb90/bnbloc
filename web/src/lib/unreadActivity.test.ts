import assert from "node:assert/strict";
import test from "node:test";
import { applyUnreadDelta, noUnread, unreadFromState } from "./unreadActivity.ts";

test("the stream's state, then new messages counted once each", () => {
  let u = unreadFromState({ unread: { r1: 2 }, channels: {} });
  assert.deepEqual(u.counts, { r1: 2 });
  ({ next: u } = applyUnreadDelta(u, { rooms: { r1: { seqs: [10, 11] }, r2: { seqs: [5] } } }));
  assert.deepEqual(u.counts, { r1: 4, r2: 1 });
  ({ next: u } = applyUnreadDelta(u, { rooms: { r1: { seqs: [11] } } }));
  assert.deepEqual(u.counts, { r1: 4, r2: 1 }, "a sequence already counted counts no more");
});

test("a deleted message it counted no longer counts; one it did not, changes nothing", () => {
  let u = unreadFromState({ unread: { r1: 1 }, channels: {} });
  ({ next: u } = applyUnreadDelta(u, { rooms: { r1: { seqs: [7] } } }));
  ({ next: u } = applyUnreadDelta(u, { rooms: { r1: { deleted: [7, 3] } } }));
  assert.deepEqual(u.counts, { r1: 1 });
});

test("a room marked read is read again; a reread replaces only its rooms", () => {
  let u = unreadFromState({ unread: { r1: 3, r2: 1 }, channels: {} });
  const { next, reread } = applyUnreadDelta(u, { rooms: { r1: { read: true } } });
  assert.deepEqual(reread, ["r1"]);
  u = unreadFromState({ unread: { r2: 9 }, channels: {} }, next, reread);
  assert.deepEqual(u.counts, { r2: 1 }, "r1 read to zero, r2 untouched");
  assert.deepEqual(unreadFromState({ unread: {}, channels: {} }, u).counts, {}, "a full state replaces everything");
  assert.deepEqual(noUnread.counts, {});
});
