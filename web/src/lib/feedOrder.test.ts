import { normalizeRoomEvent, type RoomEvent } from "./roomEvents.ts";
import type { FeedItem } from "../views/room/types.ts";
import { applyIncoming, feedItemFromEvent, maxSequence, mergeHistory } from "../views/room/feedModel.ts";

// An agent batch shares one room sequence; its messages are told apart by
// their part (index in the batch). The room sequence itself stays an
// integer everywhere: "Load earlier" sends the oldest one as ?before=, and
// the server's integer parse read a fractional cursor as "no cursor"
// (web H6).

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function rows(raw: RoomEvent): FeedItem[] {
  const out: FeedItem[] = [];
  normalizeRoomEvent(raw, (e) => {
    const item = feedItemFromEvent(e, "maya", false);
    if (item) out.push(item);
  });
  return out;
}

function batch(seq: number, texts: string[]): RoomEvent {
  return {
    kind: "agent_messages",
    sequence: seq,
    payload: {
      messages: texts.map((text) => ({ payload: { role: "assistant", text, ownerUserId: "jack", provider: "codex" } })),
    },
  };
}

const chat = (seq: number, text: string): RoomEvent => ({ kind: "chat_message", sequence: seq, payload: { userId: "maya", text } });

function testBatchKeepsTheIntegerSequence(): void {
  // Index 0 is empty and skipped: the remaining rows still carry seq 10.
  const got = rows(batch(10, ["", "first", "second"]));
  eq(
    got.map((m) => [m.seq, m.part ?? 0, m.text]),
    [
      [10, 1, "first"],
      [10, 2, "second"],
    ],
    "batch rows",
  );
  assert(
    got.every((m) => Number.isInteger(m.seq)),
    "every row's sequence is an integer",
  );
}

function testHistoryOrdersBySeqThenPart(): void {
  const live = [...rows(chat(11, "after")), ...rows(batch(10, ["b0", "b1"]))].reverse();
  const page = [...rows(chat(9, "before")), ...rows(batch(10, ["b0", "b1"]))];
  const merged = mergeHistory(live, page);
  eq(
    merged.map((m) => m.text),
    ["before", "b0", "b1", "after"],
    "merged order",
  );
  assert(maxSequence(merged) === 11, "latest sequence");
}

function testReplayedPartDedupedSiblingKept(): void {
  const [p0, p1] = rows(batch(10, ["b0", "b1"]));
  let feed = applyIncoming([], p0);
  feed = applyIncoming(feed, p1);
  feed = applyIncoming(feed, rows(batch(10, ["b0", "b1"]))[1]);
  eq(
    feed.map((m) => m.text),
    ["b0", "b1"],
    "replay deduped",
  );
}

testBatchKeepsTheIntegerSequence();
testHistoryOrdersBySeqThenPart();
testReplayedPartDedupedSiblingKept();
