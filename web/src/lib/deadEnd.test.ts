import { AssistContracts } from "./wire.gen.ts";
import {
  withHostLine,
  createCardStore,
  createReplyMemo,
  findStep,
  DEAD_END_ACTIONS,
  DEAD_END_CAUSES,
  fallbackFixes,
  FIND_A_WAY,
  isAgentReply,
  lastReplyOf,
  requestOf,
  fixLabel,
  MAX_FIXES,
  ownSettledTurn,
  parseProposal,
  type DeadEndScope,
} from "./deadEnd.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const scope: DeadEndScope = { installable: ["browser", "sheets"], agents: ["acme/maya/codex", "jack/claude"], replier: "acme/maya/codex" };

// A reply held to the closed list and the scope: unknown actions, a
// component not installable here, an agent not in the room, the agent that
// replied and a repeated fix are dropped; the rest keep their order.
{
  const p = parseProposal(
    {
      cause: "access",
      summary: "  The agent cannot reach the admin console.  ",
      fixes: [
        { action: "install", target: "browser", why: "It opens the console as you." },
        { action: "install", target: "hidden-tool", why: "Not listed." },
        { action: "launch-missiles", target: "", why: "Not an action." },
        { action: "ask-agent", target: "acme/maya/codex", why: "The same agent." },
        { action: "ask-agent", target: "olaf/codex", why: "Not in the room." },
        { action: "install", target: "browser", why: "Twice." },
        "not an object",
        { action: "ask-agent", target: "jack/claude", why: "Jack's agent can reach it." },
        { action: "rephrase", target: "ignored", why: "Say what the console is." },
        { action: "report", target: "We need a browser component.", why: "Past the bound." },
      ],
    },
    scope,
  );
  eq(p, {
    cause: "access",
    summary: "The agent cannot reach the admin console.",
    fixes: [
      { action: "install", target: "browser", why: "It opens the console as you." },
      { action: "ask-agent", target: "jack/claude", why: "Jack's agent can reach it." },
      { action: "rephrase", target: "", why: "Say what the console is." },
    ],
  }, "the proposal");
  assert(p!.fixes.length <= MAX_FIXES, "at most three fixes");
}

// An unknown cause is "other"; no summary is no proposal; no fixes is an
// empty list (the card then offers the fallback).
eq(parseProposal({ cause: "boredom", summary: "x", fixes: "install browser" }, scope), { cause: "other", summary: "x", fixes: [] }, "an unknown cause");
eq(parseProposal({ cause: "access", fixes: [] }, scope), null, "no summary");
eq(parseProposal({ summary: "x", fixes: [{ action: "attach", target: "  the Q3 sample CSV  " }, { action: "own-agent", target: "x" }] }, scope)?.fixes, [
  { action: "attach", target: "the Q3 sample CSV", why: "" },
  { action: "own-agent", target: "", why: "" },
], "an attach hint and an own-agent fix");

// The fallback: attach, ask another agent of the room (never the one that
// replied), rephrase; no other agent, no ask.
eq(fallbackFixes(scope).map((f) => `${f.action}:${f.target}`), ["attach:", "ask-agent:jack/claude", "rephrase:"], "the fallback");

// allow-host: only for the member's own sandboxed run whose settings let
// them allow a host, a host the rule takes (lower-cased) and not open yet.
{
  const fixes = [
    { action: "allow-host", target: "NPM.internal.example", why: "The registry is private." },
    { action: "allow-host", target: "169.254.169.254", why: "Metadata." },
    { action: "allow-host", target: "https://ci.internal.example", why: "A URL." },
    { action: "allow-host", target: "ci.internal.example", why: "Already open." },
  ];
  const sandboxed: DeadEndScope = { ...scope, sandboxedRun: true, mayAllowHosts: true, openHosts: ["ci.internal.example"] };
  eq(parseProposal({ summary: "x", fixes }, sandboxed)?.fixes, [{ action: "allow-host", target: "npm.internal.example", why: "The registry is private." }], "allow-host held to the rule");
  eq(parseProposal({ summary: "x", fixes }, { ...sandboxed, mayAllowHosts: false })?.fixes, [], "the organization allows only its own hosts");
  eq(parseProposal({ summary: "x", fixes }, scope)?.fixes, [], "not a sandboxed run");
  eq(fixLabel({ action: "allow-host", target: "npm.internal.example", why: "" }), "Let your agents reach npm.internal.example", "allow-host label");
  eq(fallbackFixes(sandboxed).map((f) => `${f.action}:${f.target}`), ["attach:", "ask-agent:jack/claude", "allow-host:", "rephrase:"], "the fallback asks for a host");
  eq(fallbackFixes({ ...sandboxed, mayAllowHosts: false }).some((f) => f.action === "allow-host"), false, "no host the member may not allow");
  eq(withHostLine("", "npm.internal.example"), "npm.internal.example", "the first host");
  eq(withHostLine("ci.internal.example\n", "npm.internal.example"), "ci.internal.example\nnpm.internal.example", "a host appended");
  eq(withHostLine("npm.internal.example", "npm.internal.example"), "npm.internal.example", "a host once");
}
eq(fallbackFixes({ installable: [], agents: ["acme/maya/codex"], replier: "acme/maya/codex" }).map((f) => f.action), ["attach", "rephrase"], "the fallback alone in a room");

// Once per reply: the memo keeps outcomes by room and sequence, bounded.
{
  const memo = createReplyMemo(2);
  memo.set("r1", 5, { kind: "skipped" });
  assert(memo.has("r1", 5) && !memo.has("r2", 5) && !memo.has("r1", 6), "keyed by room and reply");
  memo.set("r1", 6, { kind: "skipped" });
  memo.set("r1", 7, { kind: "skipped" });
  assert(!memo.has("r1", 5) && memo.has("r1", 7), "the oldest is forgotten past the bound");
}

// The cards live outside the room view (docs/bugs/2026-10-21-library-stale-
// after-delete-and-find-a-way-navigation.md): a find that finishes after the
// member left the room is there when they are back; a room's snapshot is the
// same object until that room's cards change; the oldest goes past the bound.
{
  const store = createCardStore(2);
  let heard = 0;
  const stop = store.subscribe(() => heard++);
  const empty = store.room("r1");
  assert(store.room("r1") === empty && Object.keys(empty).length === 0, "no cards: one stable empty snapshot");
  store.set("r1", 5, { status: "finding" });
  const finding = store.room("r1");
  assert(finding !== empty && finding[5]?.status === "finding", "a find shows at once");
  assert(store.room("r2") === store.room("r2") && !store.room("r2")[5], "another room's cards are its own");
  // The room view is gone (unsubscribed); the answer still lands.
  stop();
  store.set("r1", 5, { status: "ready", proposal: null, fixes: [], note: "Could not find a way: the assist model failed" });
  const back = store.room("r1");
  eq(back[5], { status: "ready", proposal: null, fixes: [], note: "Could not find a way: the assist model failed" }, "the failure is the card's note when the member is back");
  assert(store.room("r1") === back, "unchanged cards answer the same snapshot");
  assert(heard === 1, `listeners hear changes while subscribed (${heard})`);
  store.set("r1", 6, { status: "finding" });
  store.set("r2", 1, { status: "finding" });
  assert(!store.get("r1", 5) && !!store.get("r1", 6) && !!store.get("r2", 1), "the oldest card is forgotten past the bound");
  store.set("r1", 6, null);
  assert(!store.room("r1")[6], "dismissed");
}

// The reply's "Find a way": one assist call per reply at a time (a second
// click while it runs waits), a proposal already found shows again, no
// assist model shows the fallback.
{
  const proposal = { cause: "access" as const, summary: "s", fixes: [] };
  eq(findStep({ status: "finding" }, undefined, true), "wait", "a find still running");
  eq(findStep(undefined, { kind: "proposal", proposal }, true), "proposal", "a proposal already found");
  eq(findStep({ status: "ready", proposal: null, fixes: [] }, { kind: "skipped" }, true), "run", "asked again after the fallback");
  eq(findStep(undefined, undefined, false), "fallback", "no assist model");
}

// The gated call runs when the member's own turn settles with a reply in
// the channel they look at: not another member's, not a dispatched one,
// not a failed one, not another channel's.
{
  const job = { jobId: "j1", status: "replied", requesterUserId: "maya", channelId: "c1" };
  assert(ownSettledTurn(job, "maya", "c1"), "the member's own turn");
  assert(!ownSettledTurn(job, "Maya", "c1"), "user ids are case-sensitive");
  assert(!ownSettledTurn({ ...job, requesterUserId: "jack" }, "maya", "c1"), "another member's turn");
  assert(!ownSettledTurn({ ...job, dispatchedByJobId: "j0" }, "maya", "c1"), "a dispatched turn");
  assert(!ownSettledTurn({ ...job, status: "failed" }, "maya", "c1"), "a failed turn");
  assert(!ownSettledTurn({ ...job, channelId: "c2" }, "maya", "c1"), "another channel");
  assert(!ownSettledTurn(job, null, "c1"), "signed out");
}

// Every action has a label; an agent is named by the card.
eq(DEAD_END_ACTIONS.map((action) => fixLabel({ action, target: "browser", why: "" })), [
  "Install browser",
  "Ask",
  "Attach browser",
  "Use your own agent",
  "Say it another way",
  "Tell your admins",
  "Let your agents reach browser",
], "the labels");

// The task's output contract (wire.AssistContracts) names this closed
// list and these causes, so what any find-a-way proposes is what the card
// acts on; the card runs it gated.
{
  const contract = AssistContracts.find((c) => c.task === FIND_A_WAY);
  const fixes = contract?.fields?.find((f) => f.name === "fixes");
  eq(fixes?.item?.find((f) => f.name === "action")?.values, [...DEAD_END_ACTIONS], "the contract's actions");
  eq(contract?.fields?.find((f) => f.name === "cause")?.values, [...DEAD_END_CAUSES], "the contract's causes");
  eq(fixes?.item?.map((f) => f.name), ["action", "target", "why"], "a fix's fields");
  assert(contract?.gate === true, "the contract asks for a gate");
}

// The card goes under a job's last stored reply (never a notice, an empty
// row, a row not stored yet or another job's); the request is the member's
// own row it answers.
{
  const feed = [
    { author: "maya", text: "Pull the payouts", self: true, seq: 1 },
    { author: "agent", text: "Working on it", self: false, seq: 2, jobId: "j1", sourceSeq: 1 },
    { author: "agent", text: "I cannot reach the console.", self: false, seq: 4, jobId: "j1", sourceSeq: 1 },
    { author: "agent", text: " ", self: false, seq: 5, jobId: "j1", sourceSeq: 1 },
    { author: "agent", text: "timed out", self: false, seq: 6, jobId: "j1", notice: "member-timeout" },
    { author: "agent", text: "Other job", self: false, seq: 7, jobId: "j2", sourceSeq: 1 },
    { author: "agent", text: "Not stored yet", self: false, jobId: "j3" },
  ];
  eq(lastReplyOf(feed, "j1")?.seq, 4, "the job's last reply");
  eq(lastReplyOf(feed, "j3"), undefined, "a reply not stored yet");
  eq(requestOf(feed, feed[2])?.text, "Pull the payouts", "the request");
  eq(requestOf(feed, { author: "agent", text: "x", self: false, seq: 9, sourceSeq: 8 }), undefined, "a request not loaded");
  assert(isAgentReply(feed[2]) && !isAgentReply(feed[0]) && !isAgentReply(feed[4]), "agent replies");
}
