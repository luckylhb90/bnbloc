// A message goes to the agent it names, never to one a list happens to put first
// (AGENTS.md, "One rule, one owner": which agent). An agent is its identity: owner, core and
// provider (agentIdentity / sameAgent, lib/roomAgents.ts). A room holds many agents: one
// member's agents of several cores, the same provider under two of them, other members', and
// platform agents. So a lookup in a list of agents that asks for a *role* (mine, platform,
// online) or a *position* ([0]) does not answer "which agent": it answers "some agent", and
// sending to that one is how a request meant for nimbus's claude-code landed on another
// core's claude (2026-10-26).
//
// A use that is not a choice of recipient (a default that fills an empty pick, a label) says
// so with "which-one: <reason>" on its line. Anything else fails here.
import assert from "node:assert/strict";
import test from "node:test";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { agentIdentity, sameAgent } from "../src/lib/roomAgents.ts";
import { agentSentTo } from "../src/lib/sentRequestCard.ts";

const SRC = fileURLToPath(new URL("../src/", import.meta.url));

function sources(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) sources(path, out);
    else if (/\.(ts|tsx)$/.test(name) && !/\.(test|gen)\.tsx?$/.test(name)) out.push(path);
  }
  return out;
}

// A lookup that finds one agent by a role: self, platform, online.
const BY_ROLE = /\b(?:roomAgents|agents|offered|choices)\s*\.find\(\s*\(?\s*\w+\s*\)?\s*=>[^\n]*\b\w+\.(?:self|platform|online)\b/;
// The first agent of a list.
const FIRST = /\b(?:roomAgents|agents|recipients?|offered)\s*\[\s*0\s*\]/;

export function whichAgentViolations(text) {
  return text
    .split("\n")
    .map((line, i) => ({ line, n: i + 1 }))
    .filter(({ line }) => (BY_ROLE.test(line) || FIRST.test(line)) && !line.includes("which-one:"));
}

test("the checker finds a role lookup and a first-of-list, and honours a reason", () => {
  assert.equal(whichAgentViolations("const to = roomAgents.find((a) => a.self && !a.platform);").length, 1);
  assert.equal(whichAgentViolations("const to = roomAgents[0];").length, 1);
  assert.equal(whichAgentViolations("const to = agents.find((a) => a.self) ?? agents[0];").length, 1);
  assert.equal(whichAgentViolations("const to = roomAgents.find((a) => a.self); // which-one: fills an empty pick").length, 0);
  assert.equal(whichAgentViolations("const to = roomAgents.find((a) => sameAgent(a, sentTo));").length, 0);
  assert.equal(whichAgentViolations("const some = roomAgents.some((a) => a.self);").length, 0);
});

test("no code picks the agent of a send by role or position", () => {
  const found = [];
  for (const path of sources(SRC)) {
    for (const { line, n } of whichAgentViolations(readFileSync(path, "utf8"))) found.push(`${relative(SRC, path)}:${n}: ${line.trim()}`);
  }
  assert.deepEqual(
    found,
    [],
    `a message goes to the agent it names (agentIdentity/sameAgent), not to one a list puts first; ` +
      `if this is a default for an empty pick or a label, say so with "which-one: <reason>" on the line:\n${found.join("\n")}`,
  );
});

// The behaviour the rule protects: with several agents of one provider in the room, the one a
// request went to is found by identity, and an agent that left is nobody (never another).
test("the agent a request went to is resolved by identity, among agents of one provider", () => {
  const agents = [
    { ownerId: "maya", provider: "claude", core: "acme", self: true },
    { ownerId: "maya", provider: "claude", core: "nimbus", self: true },
    { ownerId: "maya", provider: "claude-code", core: "nimbus", self: true },
    { ownerId: "jack", provider: "claude", core: "nimbus", self: false },
  ];
  for (const a of agents) assert.equal(agentSentTo(agents, a), a, `${agentIdentity(a)} resolves to itself`);
  assert.equal(agentSentTo(agents, { ownerId: "maya", provider: "claude", core: "other" }), null, "an agent that left is nobody");
  assert.ok(!sameAgent(agents[0], agents[1]), "the core tells two agents of one provider apart");
});
