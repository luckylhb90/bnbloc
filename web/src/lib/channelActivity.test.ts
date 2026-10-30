import assert from "node:assert/strict";
import test from "node:test";
import { applyChannelActivity } from "./channelActivity.ts";

test("an activity adds its new messages to the channel's badge once", () => {
  const seen: Record<string, number> = {};
  let unread: Readonly<Record<string, number>> = { ch_a: 2 };
  unread = applyChannelActivity(unread, seen, { channelId: "ch_b", lastSeq: 10, messages: 3 });
  assert.deepEqual(unread, { ch_a: 2, ch_b: 3 });
  // The same activity again (a replay) adds nothing; a later one adds its own.
  assert.equal(applyChannelActivity(unread, seen, { channelId: "ch_b", lastSeq: 10, messages: 3 }), unread);
  unread = applyChannelActivity(unread, seen, { channelId: "ch_b", lastSeq: 12, messages: 2 });
  assert.equal(unread.ch_b, 5);
  // Activity without messages (a delete) leaves the badge.
  assert.equal(applyChannelActivity(unread, seen, { channelId: "ch_b", lastSeq: 13, messages: 0 }), unread);
  assert.equal(applyChannelActivity({ ch_c: 99 }, {}, { channelId: "ch_c", lastSeq: 1, messages: 5 }).ch_c, 100);
});
