// People chat (2026-10-08 §3): the pop-out route, the cross-window
// "Ask the agent about this" hand-off, and the people-only feed marker.
import type { RoomEvent } from "./sse.ts";
import { peopleChatRoomFromHash } from "./previewRoute.ts";
import { composerRefs, parseSharedRef, queueComposerRef } from "./team.ts";
import { feedItemFromEvent } from "../views/room/feedModel.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

// The pop-out window's route is the whole window's identity.
eq(peopleChatRoomFromHash("#/people-chat/r1"), "r1", "plain room id");
eq(peopleChatRoomFromHash("#/people-chat/a%2Fb"), "a/b", "encoded room id");
eq(peopleChatRoomFromHash("#/people-chat/"), null, "missing room id");
eq(peopleChatRoomFromHash("#/room/r1"), null, "room route is not the pop-out");
eq(peopleChatRoomFromHash("#/people-chat/r1/x"), null, "extra segment");
eq(peopleChatRoomFromHash("#/people-chat/%E0%A4%A"), null, "undecodable id");

// Queued references accumulate per room until the composer takes them.
queueComposerRef("r1", { id: 1, author: "bob", text: "first" });
queueComposerRef("r1", { id: 2, author: "carol", text: "second" });
queueComposerRef("r2", { id: 3, author: "dan", text: "other room" });
eq(composerRefs.take("r1")?.map((r) => r.text), ["first", "second"], "refs accumulate in order");
eq(composerRefs.take("r1"), undefined, "refs are taken once");
eq(composerRefs.take("r2")?.length, 1, "refs are per room");

// The cross-window payload is validated, never trusted.
eq(parseSharedRef(JSON.stringify({ roomId: "r1", ref: { id: 4, author: "bob", text: "hi" } })), { roomId: "r1", ref: { id: 4, author: "bob", text: "hi" } }, "valid shared ref");
eq(parseSharedRef(null), null, "removal event");
eq(parseSharedRef("{"), null, "malformed json");
eq(parseSharedRef(JSON.stringify({ roomId: "r1", ref: { author: 1, text: "x" } })), null, "bad author");
eq(parseSharedRef(JSON.stringify({ ref: { author: "a", text: "x" } })), null, "missing room");

// Mode A: a member message with no agent recipient is marked people-only;
// an agent-addressed one and agent replies are not.
const chat = (toAgent: boolean): RoomEvent => ({ kind: "chat_message", sequence: 5, text: "hello", author: "bob", toAgent });
eq(feedItemFromEvent(chat(false), "alice", false)?.peopleOnly, true, "people-only message is marked");
eq(feedItemFromEvent(chat(true), "alice", false)?.peopleOnly, undefined, "agent-addressed message is not marked");
eq(
  feedItemFromEvent({ kind: "agent_messages", sequence: 6, text: "reply", author: "agent" }, "alice", false)?.peopleOnly,
  undefined,
  "agent reply is not marked",
);
