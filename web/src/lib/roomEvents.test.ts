import { normalizeRoomEvent, type RoomEvent } from "./roomEvents.ts";
import { agentIdentity } from "./roomAgents.ts";
// Golden fixtures written by internal/wire's TestGoldenRoomEvents: the exact
// JSON the server sends. A field Go stops sending fails here.
import sessionState from "../../../internal/wire/testdata/agent_session_state.json" with { type: "json" };

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function normalized(ev: RoomEvent): RoomEvent[] {
  const out: RoomEvent[] = [];
  normalizeRoomEvent(structuredClone(ev), (e) => out.push(e));
  return out;
}

const reply = (core: string, notice?: string): RoomEvent =>
  normalized({
    kind: "agent_messages",
    sequence: 7,
    payload: {
      messages: [
        { payload: { role: "assistant", text: "done", ownerUserId: "maya", provider: "codex", core, ...(notice ? { notice } : {}) } },
      ],
    },
  })[0];

function testSessionStateFixtureKeysTheAgent(): void {
  const [ev] = normalized(sessionState as RoomEvent);
  assert(ev.agentPhase === "working", "the fixture is a working state");
  assert(ev.agentLabel === "maya/codex", `label ${ev.agentLabel}`);
  assert(
    ev.agentKey === agentIdentity({ ownerId: "maya", core: "acme", provider: "codex" }),
    `the indicator is keyed by the agent's identity, got ${ev.agentKey}`,
  );
}

function testReplyClearsOnlyItsOwnAgent(): void {
  const [state] = normalized(sessionState as RoomEvent);
  assert(reply("acme").agentKey === state.agentKey, "the agent's reply clears its indicator");
  assert(reply("nimbus").agentKey !== state.agentKey, "the same harness under another core is another agent");
  assert(reply("acme", "progress").agentKey === undefined, "a notice line leaves the indicator alone");
}

testSessionStateFixtureKeysTheAgent();
testReplyClearsOnlyItsOwnAgent();
