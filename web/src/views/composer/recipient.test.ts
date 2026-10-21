import { explicitRecipient, mentionedAgent, resolveRecipient } from "./recipient.ts";
import type { RoomAgent } from "./types.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const jack: RoomAgent = { ownerId: "jack", core: "acme", provider: "codex", self: false };
const jackNimbus: RoomAgent = { ownerId: "jack", core: "nimbus", provider: "codex", self: false };
const rose: RoomAgent = { ownerId: "rose", provider: "acp:hermes", self: false };
const agents = [jack, jackNimbus, rose];
const base = { roomAgents: agents, recipient: rose, localTarget: { provider: "claude", core: "acme" }, subject: "maya" };

// An in-text @handle wins over the picked recipient, by exact handle.
assert(resolveRecipient({ ...base, text: "hi @nimbus/jack/codex please" }) === jackNimbus, "the mentioned core's agent");
assert(mentionedAgent("@acme/Jack/codex", agents) === undefined, "owner ids are exact");
assert(mentionedAgent("ask @rose/acp:hermes", agents) === rose, "regex-special providers match literally");
// Else the picked recipient; else my local target as my own agent.
assert(resolveRecipient({ ...base, text: "hi" }) === rose, "the picked recipient");
const mine = resolveRecipient({ ...base, text: "hi", recipient: null });
assert(mine !== null && mine.self && mine.ownerId === "maya" && mine.provider === "claude" && mine.core === "acme", "my local target");
assert(resolveRecipient({ ...base, text: "hi", recipient: null, localTarget: undefined }) === null, "nobody to drive");
assert(explicitRecipient("hi", agents, null) === null, "a local target is not explicit");
// My local target is my own agent only once I am known: never a recipient
// with an empty owner (no bridge claims one; the server refuses it).
assert(resolveRecipient({ ...base, text: "hi", recipient: null, subject: null }) === null, "no subject: no own agent");
assert(resolveRecipient({ ...base, text: "hi", recipient: null, subject: "" }) === null, "empty subject: no own agent");
