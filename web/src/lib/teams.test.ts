import {
  defaultAgentLabel,
  estimateText,
  formatDefaultAgent,
  formatUsd,
  leadLine,
  parseDefaultAgent,
  planRequest,
  seatLine,
  teamConfirmKey,
  unstaffedRoles,
} from "./teams.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// Default agents parse and print like the validator's.
eq(parseDefaultAgent("requester"), { kind: "requester" }, "requester");
eq(parseDefaultAgent(" platform "), { kind: "platform" }, "platform");
eq(parseDefaultAgent("member:jack"), { kind: "member", owner: "jack" }, "member");
eq(parseDefaultAgent("member:jack/codex"), { kind: "member", owner: "jack", provider: "codex" }, "member provider");
for (const bad of ["", "me", "member:", "member:/x", "member:jack/", "member:a b", "member:a/b/c"]) {
  eq(parseDefaultAgent(bad), null, `bad ${JSON.stringify(bad)}`);
}
eq(formatDefaultAgent({ kind: "member", owner: "jack", provider: "codex" }), "member:jack/codex", "format member");
eq(formatDefaultAgent({ kind: "platform" }), "platform", "format platform");
eq(defaultAgentLabel("requester"), "your own agent", "label requester");
eq(defaultAgentLabel("member:jack/codex", (id) => (id === "jack" ? "Jack" : id)), "Jack's codex", "label member");
eq(defaultAgentLabel("weird"), "weird", "label unparsed");

// The plan request carries the lead and each role's default agent.
eq(
  planRequest({ lead: "lead", members: [{ role: "reviewer", expert: "code-reviewer@^1", defaultAgent: "platform" }] }, { ownerId: "maya", provider: "opencode" }),
  { lead: { ownerId: "maya", provider: "opencode" }, members: [{ role: "reviewer", defaultAgent: "platform" }] },
  "plan request",
);

// The estimate is honest: a sum and its basis, unknown seats named.
eq(formatUsd(0.42), "$0.42", "usd");
eq(formatUsd(0.0042), "$0.0042", "small usd");
eq(estimateText({ costUsd: 0.42, runs: 37 }), "About $0.42 per request, based on the last 37 runs.", "estimate");
eq(estimateText({ costUsd: 0.3, runs: 1, unknown: ["tester"] }), "About $0.30 per request, based on the last run; no cost data for tester.", "partial");
eq(estimateText({ runs: 0, unknown: ["lead", "reviewer"] }), "Cost unknown: none of these agents reported a cost on recent runs.", "unknown");

const plan = {
  lead: { online: true, agent: { ownerId: "maya", provider: "opencode", handle: "maya/opencode" } },
  roles: [
    { role: "reviewer", online: true, agent: { ownerId: "bob", provider: "claude", handle: "bob/claude" } },
    { role: "tester", online: false, reason: "no platform agent is added to this room" },
  ],
  estimate: { runs: 0 },
};
eq(seatLine(plan.roles[0]), "reviewer → bob/claude", "staffed seat");
eq(seatLine(plan.roles[1]), "tester → unavailable (no platform agent is added to this room)", "unstaffed seat");
eq(unstaffedRoles(plan), ["tester"], "unstaffed roles");
eq(teamConfirmKey("cc_t", "1.0.0", { ownerId: "maya", provider: "opencode" }), "cc_t@1.0.0:maya//opencode", "confirm key");
eq(teamConfirmKey("cc_t", "1.0.0", null), "", "no lead");

// The plan names its lead, and a platform lead's host (the one the send uses).
eq(leadLine(plan), "Lead: maya/opencode", "own lead");
eq(
  leadLine({ lead: { online: true, agent: { ownerId: "host-b", core: "nimbus", provider: "codex", handle: "host-b/codex", platform: true } } }, (id) => (id === "host-b" ? "Host B" : id)),
  "Lead: codex, a platform agent on Host B's machine",
  "platform lead names its host",
);
eq(leadLine({ lead: { online: false, reason: "jack/codex is not online in this room", agent: { ownerId: "jack", provider: "codex", handle: "jack/codex" } } }), "Lead: jack/codex (jack/codex is not online in this room)", "offline lead");
