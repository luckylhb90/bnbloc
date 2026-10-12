import { restoreAgent, savedAgent } from "./agentPrefs.ts";
import type { RoomAgent } from "../views/composer/types.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const jacks: RoomAgent = { ownerId: "jack", core: "acme", provider: "codex", self: false };
const mayas: RoomAgent = { ownerId: "maya", core: "acme", provider: "codex", self: true };
const mayasNimbus: RoomAgent = { ownerId: "maya", core: "nimbus", provider: "codex", self: true };
// The roster is not self-first: jack's agent of the same harness comes first.
const roster = [jacks, mayas, mayasNimbus];
const target = { id: "acme/local:codex", core: "acme", provider: "codex" };

// web H9: maya's saved own agent never restores onto jack's agent of the
// same (core, provider), which would start a borrow.
eq(
  restoreAgent({ ownerId: "maya", core: "acme", provider: "codex" }, roster, [target], "maya"),
  { ownTargetId: target.id },
  "my own agent restores my local target",
);
eq(
  restoreAgent({ ownerId: "maya", core: "acme", provider: "codex" }, roster, [], "maya"),
  { recipient: mayas },
  "without a local target, my own advertised agent, never jack's",
);
eq(
  restoreAgent({ ownerId: "maya", core: "nimbus", provider: "codex" }, roster, [target], "maya"),
  { recipient: mayasNimbus },
  "the core is part of the identity",
);
eq(
  restoreAgent({ ownerId: "jack", core: "acme", provider: "codex" }, roster, [target], "maya"),
  { recipient: jacks },
  "a saved borrow restores exactly that member's agent",
);
eq(restoreAgent({ ownerId: "bob", core: "acme", provider: "codex" }, roster, [target], "maya"), null, "an agent no longer in the room restores nothing");
eq(restoreAgent({ core: "acme", provider: "codex" }, roster, [target], "maya"), null, "a row without an owner restores nothing");

eq(savedAgent(jacks, target, "maya"), { ownerId: "jack", core: "acme", provider: "codex" }, "a picked room agent is saved with its owner");
eq(savedAgent(null, target, "maya"), { ownerId: "maya", core: "acme", provider: "codex" }, "my own target is saved as mine");
eq(savedAgent(null, undefined, "maya"), null, "nothing picked, nothing saved");
