import { excerpt, sourceAuthor, sourceText } from "./forward.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const chat = { kind: "chat_message" as const, payload: { userId: "maya", text: "ship on friday" } };
const agent = {
  kind: "agent_messages" as const,
  payload: { messages: [{ payload: { ownerUserId: "jack", provider: "codex", text: " first " } }, { payload: { text: "second" } }, { payload: {} }] },
};
eq(sourceText(chat), "ship on friday", "a person's text");
eq(sourceAuthor(chat), "maya", "a person");
eq(sourceText(agent), "first\nsecond", "an agent reply's messages joined");
eq(sourceAuthor(agent), "jack/codex", "an agent");
eq(sourceText({ kind: "chat_message", payload: {} }), "", "no text");
eq(excerpt("x".repeat(300)).length, 280, "an excerpt is bounded");
eq(excerpt("  short  "), "short", "trimmed");
console.log("forward tests passed");
