import { applyMark, isPinned, isSaved, pinnedIn, reactionsOf } from "./messageMarks.ts";
import type { MessageMark } from "./wire.gen.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const r = (seq: number, userId: string, value: string): MessageMark => ({ seq, kind: "reaction", userId, value, by: userId, createdAtMs: 1, channelId: "ch" });
const pin = (seq: number, channelId: string): MessageMark => ({ seq, kind: "pin", by: "erin", createdAtMs: 1, channelId });

let marks: MessageMark[] = [];
marks = applyMark(marks, { mark: r(7, "maya", "👍") });
marks = applyMark(marks, { mark: r(7, "maya", "👍") });
marks = applyMark(marks, { mark: r(7, "jack", "👍") });
marks = applyMark(marks, { mark: r(7, "jack", "🎉") });
eq(reactionsOf(marks, 7, "maya"), [{ value: "👍", count: 2, mine: true }, { value: "🎉", count: 1, mine: false }], "counted by emoji, mine marked, applied twice once");
eq(reactionsOf(marks, 7, "Maya"), [{ value: "👍", count: 2, mine: false }, { value: "🎉", count: 1, mine: false }], "a subject is compared exactly");
marks = applyMark(marks, { mark: r(7, "maya", "👍"), removed: true });
eq(reactionsOf(marks, 7, "maya"), [{ value: "👍", count: 1, mine: false }, { value: "🎉", count: 1, mine: false }], "a removal takes only that member's");

marks = applyMark(marks, { mark: pin(7, "ch") });
marks = applyMark(marks, { mark: pin(3, "other") });
eq(isPinned(marks, 7), true, "pinned");
eq(pinnedIn(marks, "ch").map((m) => m.seq), [7], "a channel's pins only");
marks = applyMark(marks, { mark: { seq: 3, kind: "saved", userId: "maya", by: "maya", createdAtMs: 1 } });
eq(isSaved(marks, 3, "maya"), true, "maya saved it");
eq(isSaved(marks, 3, "jack"), false, "jack did not");
eq(isSaved(marks, 3, null), false, "nobody signed in");
console.log("messageMarks tests passed");
