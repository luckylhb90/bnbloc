import type { RoomEvent } from "./sse.ts";
import type { FeedItem } from "../views/room/types.ts";
import {
  ackSent,
  applyIncoming,
  feedItemFromEvent,
  markStatus,
  maxSequence,
  mergeHistory,
  outgoingMessage,
} from "../views/room/feedModel.ts";
import { unreadBaseline } from "./unreadBaseline.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const me = "alice";

function pending(id: number, text: string, clientMsgId: string): FeedItem {
  return {
    id,
    author: me,
    text,
    self: true,
    status: "pending",
    clientMsgId,
    outgoing: outgoingMessage(text, null, "ch1"),
  };
}

function echo(seq: number, text: string, extra: Partial<RoomEvent> = {}): FeedItem {
  const item = feedItemFromEvent({ kind: "chat_message", sequence: seq, author: me, text, ...extra }, me, true);
  if (!item) throw new Error("echo produced no row");
  return item;
}

function testAckThenEchoConfirmsOneRow(): void {
  let feed: FeedItem[] = [pending(1, "hi", "c1")];
  feed = ackSent(feed, 1, 7);
  assert(feed.length === 1 && feed[0].seq === 7, "ack records the server sequence on the optimistic row");
  assert(feed[0].status === "pending", "the row stays pending until the stream echo");
  feed = applyIncoming(feed, echo(7, "hi"));
  assert(feed.length === 1, "the echo of an acknowledged send never adds a second row");
  assert(feed[0].status === "sent" && feed[0].id === 1, "the echo confirms the optimistic row in place");
}

function testEchoBeforeAckMergesIntoTheOptimisticRow(): void {
  let feed: FeedItem[] = [pending(1, "hi", "c1"), { id: 2, author: "bob", text: "yo", self: false, seq: 6 }];
  feed = applyIncoming(feed, echo(7, "hi"));
  assert(feed.length === 3, "an echo that cannot be matched yet is appended");
  feed = ackSent(feed, 1, 7);
  assert(feed.length === 2, "the ack merges the early echo into the optimistic row");
  const row = feed.find((m) => m.seq === 7);
  assert(row?.id === 1 && row.status === "sent", "the optimistic row keeps its identity and is confirmed");
}

// The server's echo carries the clientMsgId, so an echo that beats the POST
// response confirms the row in place; the late ack must not undo that.
function testClientMsgIdEchoBeforeAckStaysConfirmed(): void {
  let feed: FeedItem[] = [pending(1, "hi", "c1")];
  feed = applyIncoming(feed, echo(7, "hi", { clientMsgId: "c1" }));
  assert(feed.length === 1 && feed[0].status === "sent" && feed[0].seq === 7, "the echo confirms the row by clientMsgId");
  feed = ackSent(feed, 1, 7);
  assert(feed.length === 1 && feed[0].status === "sent" && feed[0].seq === 7, "the late ack keeps the confirmed row sent");
}

function testEchoCarryingClientMsgIdReconcilesWithoutAck(): void {
  let feed: FeedItem[] = [markStatus([pending(1, "hi", "c1")], 1, "failed")[0]];
  feed = applyIncoming(feed, echo(9, "hi", { clientMsgId: "c1" }));
  assert(feed.length === 1 && feed[0].seq === 9 && feed[0].status === "sent", "clientMsgId identifies a failed-but-delivered send");
}

function testNoTextHeuristic(): void {
  let feed: FeedItem[] = [pending(1, "same text", "c1")];
  feed = applyIncoming(feed, echo(5, "same text"));
  assert(feed.length === 2, "identical text alone never reconciles (identity is seq or clientMsgId)");
}

function testReplayIsDeduped(): void {
  let feed: FeedItem[] = [{ id: 3, author: "bob", text: "x", self: false, seq: 4 }];
  const next = applyIncoming(feed, { id: 4, author: "bob", text: "x", self: false, seq: 4 });
  assert(next === feed, "a replayed sequence is a no-op");
  feed = next;
}

function testHistoryMergeOrdersBySequence(): void {
  const live: FeedItem[] = [
    { id: 10, author: "bob", text: "live", self: false, seq: 12 },
    pending(11, "unsent", "c2"),
  ];
  const page: FeedItem[] = [
    { id: 20, author: "bob", text: "old", self: false, seq: 3 },
    { id: 21, author: "bob", text: "dup", self: false, seq: 12 },
    { id: 22, author: "bob", text: "mid", self: false, seq: 8 },
  ];
  const merged = mergeHistory(live, page);
  assert(merged.map((m) => m.seq ?? "-").join(",") === "3,8,12,-", "history is merged in sequence order, unsent rows last");
  assert(merged.find((m) => m.seq === 12)?.id === 10, "an already-present sequence keeps the live row");
}

function testOutgoingCapturesTheExactRequest(): void {
  const out = outgoingMessage(
    "do it",
    { ownerId: "bob", provider: "codex", core: "corex", self: false, agentOptions: { model: "m1" } },
    "ch9",
    ["att1"],
  );
  assert(out.borrow, "another member's agent is a borrow");
  assert(out.recipient?.ownerId === "bob" && out.recipient.core === "corex", "recipient owner and core are kept");
  assert(out.recipient?.agentOptions?.model === "m1", "per-send options are kept for retry");
  assert(out.channelId === "ch9" && out.attachmentIds[0] === "att1", "channel and attachments are kept");
  const own = outgoingMessage("hi", { ownerId: me, provider: "codex", self: true }, "ch1");
  assert(!own.borrow, "my own agent is not a borrow");
  assert(outgoingMessage("hi", null, "ch1").recipient === null, "people-only has no recipient");
}

function testUnreadBaseline(): void {
  const feed = [{ seq: 1 }, { seq: 2 }, { seq: 3 }, { seq: 4 }];
  assert(unreadBaseline(feed, 0) === null, "no unread, no divider");
  assert(unreadBaseline(feed, 2) === 3, "divider above the first of the last N messages");
  assert(unreadBaseline(feed, 9) === 1, "unread beyond the loaded page starts at the oldest loaded");
  assert(unreadBaseline([], 3) === null, "no loaded rows, no divider");
  assert(maxSequence([{ seq: 2 }, { seq: 7.002 }, {}]) === 7, "latest sequence floors fractional agent offsets");
}

testAckThenEchoConfirmsOneRow();
testEchoBeforeAckMergesIntoTheOptimisticRow();
testClientMsgIdEchoBeforeAckStaysConfirmed();
testEchoCarryingClientMsgIdReconcilesWithoutAck();
testNoTextHeuristic();
testReplayIsDeduped();
testHistoryMergeOrdersBySequence();
testOutgoingCapturesTheExactRequest();
testUnreadBaseline();

function testScheduledMarkerReachesTheRow(): void {
  const row = feedItemFromEvent({ kind: "chat_message", sequence: 3, author: "bob", text: "standup", scheduledId: "sch_1" }, me, false);
  assert(row?.scheduledId === "sch_1", "a scheduled post keeps its schedule id for the badge");
  assert(echo(4, "typed").scheduledId === undefined, "a typed message carries no schedule id");
}
testScheduledMarkerReachesTheRow();

function testDispatchAttributionReachesTheRow(): void {
  const dispatch = { jobId: "job_parent", depth: 1, via: "jack/codex", to: "bob/claude" };
  const row = feedItemFromEvent({ kind: "chat_message", sequence: 5, author: "maya", text: "review it", dispatch }, me, true);
  assert(row?.dispatch?.via === "jack/codex" && row.dispatch.to === "bob/claude", "a dispatched message keeps its attribution");
  assert(row?.author === "maya", "a dispatched message is its requester's");
  assert(echo(6, "typed").dispatch === undefined, "a typed message carries no dispatch attribution");
}
testDispatchAttributionReachesTheRow();
