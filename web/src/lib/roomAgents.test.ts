import { agentHandle, agentIdentity, borrowKey, borrowTarget, mergeRoomAgents, parseAgentHandle, sameAgent } from "./roomAgents.ts";
import { agentRows, rowIsActive } from "../views/composer/agentRows.ts";
import { roomIdProblem } from "./roomId.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function testBorrowIdentityMatchesTheBridge(): void {
  assert(borrowTarget({ core: "corex", provider: "codex" }) === "shared-agent:corex:codex", "core-qualified target, colon separated");
  assert(borrowTarget({ provider: "codex" }) === "shared-agent:codex", "coreless target is the bare provider");
  assert(borrowKey({ core: "corex", provider: "acp:hermes" }) === "corex:acp:hermes", "providers with colons stay intact");
}

function testMergeRoomAgentsDedupesGrantsAgainstTheRoster(): void {
  const agents = mergeRoomAgents(
    [
      { ownerUserId: "bob", core: "corex", provider: "codex" },
      { ownerUserId: "alice", core: "corex", provider: "codex" },
    ],
    [
      { ownerUserId: "bob", provider: "corex:codex" }, // the same agent, as authorized
      { ownerUserId: "carol", provider: "codex" }, // not advertised right now
      { ownerUserId: "alice", provider: "corex:codex" }, // mine: never borrowable
    ],
    "alice",
  );
  assert(agents.length === 3, "a grant for an advertised agent is not a second row");
  assert(agents.find((a) => a.ownerId === "alice")?.self === true, "self is matched");
  assert(agents.some((a) => a.ownerId === "carol" && !a.self), "authorized-but-offline agents stay discoverable");
}

function testMergeRoomAgentsCarriesTheSandboxBadge(): void {
  const agents = mergeRoomAgents(
    [
      { ownerUserId: "bob", core: "corex", provider: "claude", sandboxed: true },
      { ownerUserId: "bob", core: "corex", provider: "codex" },
    ],
    [],
    "alice",
  );
  assert(agents[0]?.sandboxed === true, "a sandboxed roster agent keeps its badge");
  assert(agents[1]?.sandboxed === undefined, "an unsandboxed agent carries no badge field");
}

function testPickerKeepsOwnersApart(): void {
  const groups = agentRows(
    [{ id: "corex/local:codex", core: "corex", provider: "codex" }],
    [
      { ownerId: "alice", core: "corex", provider: "codex", self: true },
      { ownerId: "bob", core: "corex", provider: "codex", self: false },
    ],
    "alice",
  );
  const rows = groups.flatMap((g) => g.rows);
  assert(rows.length === 2, "my local target and my advertised agent are one row; bob's is another");
  const mine = rows.find((r) => r.self);
  const bobs = rows.find((r) => !r.self);
  assert(!!mine?.localTargetId && !!mine.agent, "my row drives the local target and knows its roster entry");
  assert(bobs?.ownerId === "bob" && bobs.agent?.ownerId === "bob", "bob's agent is selectable as a borrow");
  assert(mine!.key !== bobs!.key, "rows are keyed by owner, core and provider");
  assert(rowIsActive(bobs!, bobs!.agent!, "corex/local:codex"), "a picked recipient activates its own row");
  assert(!rowIsActive(mine!, bobs!.agent!, "corex/local:codex"), "…and not my row of the same harness");
  assert(rowIsActive(mine!, null, "corex/local:codex"), "with no recipient my selected target is active");
  assert(agentIdentity({ ownerId: "Bob", core: "X", provider: "Codex" }) === "Bob/X/Codex", "identity is exact (subjects are case-sensitive)");
}

function testRoomIdRule(): void {
  assert(roomIdProblem("team-room") === null, "a plain id is valid");
  assert(roomIdProblem("  ") !== null, "empty is rejected");
  assert(roomIdProblem("a/b") !== null && roomIdProblem("a%b") !== null && roomIdProblem("a#b") !== null, "URL-significant characters are rejected");
  assert(roomIdProblem("a\\b") !== null && roomIdProblem("a?b") !== null, "backslash and ? are rejected");
  assert(roomIdProblem("a\u0007b") !== null, "control characters are rejected");
  assert(roomIdProblem("x".repeat(64)) === null && roomIdProblem("x".repeat(65)) !== null, "64 bytes is the limit");
  assert(roomIdProblem("é".repeat(33)) !== null, "the limit counts UTF-8 bytes, not characters");
}

testBorrowIdentityMatchesTheBridge();
testMergeRoomAgentsDedupesGrantsAgainstTheRoster();
testMergeRoomAgentsCarriesTheSandboxBadge();
testPickerKeepsOwnersApart();
testRoomIdRule();

// One handle syntax, read back by one parser: the core keeps two cores' agents
// apart, and owner/core/provider compare exactly (web M12, M13).
function testHandleRoundTrip(): void {
  for (const a of [{ ownerId: "maya", provider: "codex" }, { ownerId: "maya", core: "acme", provider: "codex" }]) {
    const back = parseAgentHandle(agentHandle(a));
    assert(back !== null && sameAgent(back, a), `round trip ${agentHandle(a)}`);
  }
  assert(agentHandle({ ownerId: "maya", core: "acme", provider: "codex" }) === "acme/maya/codex", "core first");
  assert(parseAgentHandle("maya") === null && parseAgentHandle("a/b/c/d") === null && parseAgentHandle("maya/") === null, "not handles");
  assert(!sameAgent({ ownerId: "maya", core: "acme", provider: "codex" }, { ownerId: "maya", core: "nimbus", provider: "codex" }), "two cores");
  assert(!sameAgent({ ownerId: "maya", provider: "codex" }, { ownerId: "Maya", provider: "codex" }), "exact owner");
}
testHandleRoundTrip();
