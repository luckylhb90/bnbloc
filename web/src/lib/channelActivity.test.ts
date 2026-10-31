import assert from "node:assert/strict";
import test from "node:test";
import { addChannelActivity, takeChannelActivity } from "./channelActivity.ts";

test("an activity adds its new messages to the channel's badge once", () => {
  const seen: Record<string, number> = {};
  const apply = (unread: Readonly<Record<string, number>>, a: Parameters<typeof takeChannelActivity>[1]) => {
    const taken = takeChannelActivity(seen, a);
    return taken ? addChannelActivity(unread, taken) : unread;
  };
  let unread: Readonly<Record<string, number>> = { ch_a: 2 };
  unread = apply(unread, { channelId: "ch_b", lastSeq: 10, messages: 3 });
  assert.deepEqual(unread, { ch_a: 2, ch_b: 3 });
  // The same activity again (a replay) adds nothing; a later one adds its own.
  assert.equal(apply(unread, { channelId: "ch_b", lastSeq: 10, messages: 3 }), unread);
  unread = apply(unread, { channelId: "ch_b", lastSeq: 12, messages: 2 });
  assert.equal(unread.ch_b, 5);
  // Activity without messages (a delete) leaves the badge.
  assert.equal(apply(unread, { channelId: "ch_b", lastSeq: 13, messages: 0 }), unread);
  assert.equal(addChannelActivity({ ch_c: 99 }, { channelId: "ch_c", messages: 5 }).ch_c, 100);
});

test("adding a taken activity is pure: a state updater called twice gives one answer", () => {
  const seen: Record<string, number> = {};
  const taken = takeChannelActivity(seen, { channelId: "ch_b", lastSeq: 4, messages: 2 });
  assert.ok(taken);
  const before = { ch_b: 1 };
  assert.deepEqual(addChannelActivity(before, taken), addChannelActivity(before, taken));
  assert.deepEqual(before, { ch_b: 1 });
});
