import {
  agentFollowUps,
  clearTemplateStart,
  emptyTemplateBody,
  parseAgentLines,
  parseLines,
  rememberTemplateStart,
  takeTemplateStart,
  templateSummary,
  type RoomTemplateInstance,
} from "./roomTemplates.ts";
import { agentHandle } from "./roomAgents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq(templateSummary(emptyTemplateBody()), "Empty room", "empty summary");
eq(
  templateSummary({ ...emptyTemplateBody(), channels: ["a", "b"], promptPacks: [{ componentId: "cc_1" }], schedules: [] }),
  "2 channels · 1 prompt pack",
  "summary",
);

// Suggested agents never authorize: every agent I do not own needs a borrow
// request; owners who are not members are offered as invites (once each).
const agents = [
  { ownerId: "me", provider: "codex" },
  { ownerId: "bob", provider: "codex" },
  { ownerId: "bob", provider: "claude" },
  { ownerId: "carol", provider: "codex" },
];
eq(
  agentFollowUps(agents, "me", ["me", "carol"]),
  { invite: ["bob"], requestBorrow: [agents[1], agents[2], agents[3]] },
  "follow-ups",
);

const mem = new Map<string, string>();
const storage = {
  getItem: (k: string) => mem.get(k) ?? null,
  setItem: (k: string, v: string) => void mem.set(k, v),
  removeItem: (k: string) => void mem.delete(k),
};
const inst: RoomTemplateInstance = {
  roomId: "r1",
  name: "R1",
  templateId: "tpl_1",
  channels: [],
  addedMembers: [],
  schedules: [],
  promptPacks: ["cc_1"],
  suggestedAgents: [],
};
rememberTemplateStart(inst, storage);
eq(takeTemplateStart("r1", storage)?.promptPacks, ["cc_1"], "remembered");
clearTemplateStart("r1", storage);
eq(takeTemplateStart("r1", storage), undefined, "cleared");

eq(parseLines(" a \n\nb\na\n"), ["a", "b"], "lines");
eq(agentHandle({ ownerId: "bob", provider: "codex", core: "c1" }), "c1/bob/codex", "agent line with core");
eq(
  parseAgentLines("bob/codex\nc1/carol/claude\nnope\na//b\n"),
  {
    agents: [
      { ownerId: "bob", provider: "codex" },
      { core: "c1", ownerId: "carol", provider: "claude" },
    ],
    invalid: ["nope", "a//b"],
  },
  "agent lines",
);
