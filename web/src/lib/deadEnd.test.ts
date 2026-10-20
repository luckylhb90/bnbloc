import findAWay from "../../../internal/contentbuiltin/packages/find-a-way/component.json" with { type: "json" };
import {
  createReplyMemo,
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
], "the labels");

// The built-in task names this closed list and these causes, so what it
// proposes is what the card acts on.
{
  assert(findAWay.name === FIND_A_WAY, "the built-in's name");
  const fixes = findAWay.fields.find((f) => f.name === "fixes");
  const action = fixes?.item?.find((f) => f.name === "action")?.description ?? "";
  const cause = findAWay.fields.find((f) => f.name === "cause")?.description ?? "";
  eq(action.split(/,\s*|\s+or\s+/), [...DEAD_END_ACTIONS], "the task's actions");
  eq(cause.split(/,\s*|\s+or\s+/), [...DEAD_END_CAUSES], "the task's causes");
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
