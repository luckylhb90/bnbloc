import { normalizeRoomEvent, type RoomEvent } from "../../lib/roomEvents.ts";
import type { FeedItem } from "./types.ts";
import { applyIncoming, feedItemFromEvent, mergeHistory } from "./feedModel.ts";
import { aboutRequest, blockProgress, blockTitle, layoutTurns, progressText, renderOrder, unreadDividerAt, type FeedEntry } from "./feedTurns.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const me = "maya";

// The room's events of one team turn, as the server publishes them (and as
// history returns them): maya asks the lead (jack/codex) with a team; the
// lead posts a progress line, dispatches to the writer (bob/claude) and the
// critic (ann/opencode), each replies, the critic's dispatch fails a nested
// dispatch, and the lead answers.
const chat = (seq: number, text: string, extra: Record<string, unknown> = {}): RoomEvent => ({
  kind: "chat_message",
  sequence: seq,
  payload: { userId: me, text, recipient: { ownerId: "jack", provider: "codex" }, ...extra },
});
const dispatch = (seq: number, parentSeq: number, to: [string, string], role?: string, depth = 1): RoomEvent =>
  chat(seq, `task ${seq}`, {
    recipient: { ownerId: to[0], provider: to[1] },
    dispatchedBy: { jobId: `job_${parentSeq}`, parentSeq, depth, via: "jack/codex", ...(role ? { role } : {}) },
  });
const agent = (seq: number, owner: string, provider: string, sourceSeq: number, text: string, notice?: string): RoomEvent => ({
  kind: "agent_messages",
  sequence: seq,
  payload: {
    messages: [{ payload: { role: "assistant", text, ownerUserId: owner, provider, jobId: `job_${sourceSeq}`, sourceSeq, ...(notice ? { notice } : {}) } }],
  },
});

const events: RoomEvent[] = [
  chat(10, "write a haiku about rain"),
  agent(11, "jack", "codex", 10, "Phase 1/2: writer", "progress"),
  dispatch(12, 10, ["bob", "claude"], "writer"),
  agent(13, "bob", "claude", 12, "rain on a tin roof"),
  dispatch(14, 10, ["ann", "opencode"], "critic"),
  dispatch(15, 14, ["bob", "claude"], undefined, 2),
  agent(16, "bob", "claude", 15, "nested answer"),
  agent(17, "ann", "opencode", 14, "9/10"),
  chat(18, "an unrelated question", { recipient: { ownerId: "zoe", provider: "codex" } }),
  agent(19, "jack", "codex", 10, "Here is the haiku. Critic: 9/10"),
  agent(20, "zoe", "codex", 18, "unrelated answer"),
];

function items(evs: readonly RoomEvent[], live: boolean): FeedItem[] {
  const out: FeedItem[] = [];
  for (const raw of evs) {
    normalizeRoomEvent(structuredClone(raw), (e) => {
      const it = feedItemFromEvent(e, me, live);
      if (it) out.push(it);
    });
  }
  return out;
}

// Live: each event arrives on the stream in order.
let live: FeedItem[] = [];
for (const it of items(events, true)) live = applyIncoming(live, it);
// Reload: the same events come back as history pages, newest page first.
let reloaded: FeedItem[] = [];
reloaded = mergeHistory(reloaded, items(events.slice(6), false));
reloaded = mergeHistory(reloaded, items(events.slice(0, 6), false));

// The layout in sequence terms: rows by seq, blocks as [anchorSeq, [[seq, level]...]].
// (By time has no request blocks; feedRequests.test.ts covers By request.)
function shape(feed: readonly FeedItem[], entries: readonly FeedEntry[]): unknown[] {
  const s = (i: number) => feed[i].seq ?? 0;
  return entries.map((e) => {
    if (e.kind === "row") return s(e.index);
    if (e.kind === "request") throw new Error("by time has no request blocks");
    return [s(e.block.anchor), e.block.rows.map((r) => [s(r.index), r.level])];
  });
}

const want = [
  10,
  [
    10,
    [
      [11, 0], // the lead's progress line
      [12, 0], // writer dispatch
      [13, 1], // its reply
      [14, 0], // critic dispatch
      [15, 1], // the critic's own dispatch
      [16, 2], // its reply
      [17, 1], // the critic's reply
    ],
  ],
  18,
  19, // the lead's answer, after the block
  20,
];
eq(shape(live, layoutTurns(live)), want, "live layout");
eq(shape(reloaded, layoutTurns(reloaded)), want, "reloaded layout matches live");

const block = layoutTurns(live).find((e) => e.kind === "block");
assert(block?.kind === "block", "a block exists");
if (block?.kind === "block") {
  eq(block.block.roles, ["writer", "critic"], "team roles");
  eq(block.block.agents, ["bob/claude", "ann/opencode"], "agents");
  eq(blockTitle(block.block), "Team · writer, critic", "team title");
  const statuses: Record<number, string> = { 12: "replied", 14: "running", 15: "replied" };
  const p = blockProgress(block.block, (i) => statuses[live[i].seq ?? 0]);
  eq(p, { done: 2, total: 3, active: true }, "progress while the critic runs");
  eq(progressText(p), "2 of 3 done", "progress text while running");
  const settled = blockProgress(block.block, () => "replied");
  eq(progressText(settled), "All 3 done", "all settled");
  // An old turn whose jobs the room's job list no longer holds is neither
  // working nor done: the block stays folded.
  const unknown = blockProgress(block.block, () => undefined);
  eq(unknown, { done: 0, total: 3, active: false }, "unknown jobs");
  eq(progressText(unknown), "3 tasks", "unknown jobs text");
  eq(renderOrder(layoutTurns(live)).map((i) => live[i].seq ?? 0), [10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20], "render order");
}

// A plain dispatch (no team role) nests the same way, titled by its agent.
{
  const plain = items([chat(1, "check this"), dispatch(2, 1, ["bob", "claude"]), agent(3, "bob", "claude", 2, "checked"), agent(4, "jack", "codex", 1, "done")], true);
  const entries = layoutTurns(plain);
  eq(shape(plain, entries), [1, [1, [[2, 0], [3, 1]]], 4], "plain delegation");
  const b = entries.find((e) => e.kind === "block");
  if (b?.kind === "block") eq(blockTitle(b.block), "Delegated to bob/claude", "plain title");
}

// A request without dispatches renders flat, notices included.
{
  const flat = items([chat(1, "hi"), agent(2, "jack", "codex", 1, "Phase 1", "progress"), agent(3, "jack", "codex", 1, "hello")], true);
  eq(shape(flat, layoutTurns(flat)), [1, 2, 3], "nothing delegated");
}

// A history page without the request keeps its rows in place; loading the
// earlier page groups them.
{
  let feed: FeedItem[] = mergeHistory([], items(events.slice(2, 5), false));
  eq(shape(feed, layoutTurns(feed)), [12, 13, 14], "request not loaded: flat");
  feed = mergeHistory(feed, items(events.slice(0, 2), false));
  eq(shape(feed, layoutTurns(feed)), [10, [10, [[11, 0], [12, 0], [13, 1], [14, 0]]]], "earlier page loaded: grouped");
}

// Rows without the payload chain (no parentSeq / sourceSeq) stay flat.
{
  const old = items(
    [
      chat(1, "go"),
      { kind: "chat_message", sequence: 2, payload: { userId: me, text: "task", recipient: { ownerId: "bob", provider: "claude" }, dispatchedBy: { jobId: "job_1", depth: 1, via: "jack/codex" } } },
      { kind: "agent_messages", sequence: 3, payload: { messages: [{ payload: { role: "assistant", text: "ok", ownerUserId: "bob", provider: "claude" } }] } },
    ],
    true,
  );
  eq(shape(old, layoutTurns(old)), [1, 2, 3], "no chain: flat");
}

// A late failure notice sits back at its request, naming it
// (docs/bugs/2026-10-21-late-failure-notice-and-ownerless-recipient.md): #7's
// job failed minutes later, after #8–#12 were posted and answered; its notice
// (#13) renders at the end of #7's turn, before #8, never under #12.
{
  const qa: RoomEvent[] = [
    chat(7, "hello, please read the attached file and tell me how many lines it has"),
    chat(8, "please read the attached 10MB file and tell me how many lines it has"),
    agent(9, "jack", "codex", 8, "The file has 132,732 lines."),
    chat(10, "please query the internal system"),
    agent(11, "jack", "codex", 10, "I cannot reach that host."),
    chat(12, "open https://example.com and read its heading"),
    agent(13, "", "opencode", 7, "The opencode agent is offline: no bridge picked up this request.", "job-failed"),
  ];
  const liveQa = items(qa, true).reduce<FeedItem[]>((f, it) => applyIncoming(f, it), []);
  const reloadedQa = mergeHistory(mergeHistory([], items(qa.slice(3), false)), items(qa.slice(0, 3), false));
  for (const [name, feed] of [["live", liveQa], ["reloaded", reloadedQa]] as const) {
    const entries = layoutTurns(feed);
    eq(shape(feed, entries), [7, 13, 8, 9, 10, 11, 12], `${name}: the late notice is at its request`);
    const moved = entries[1];
    assert(moved.kind === "row" && moved.about !== undefined && feed[moved.about].seq === 7, `${name}: the moved notice names #7`);
    assert(entries.filter((e) => e.kind === "row" && e.about !== undefined).length === 1, `${name}: only the late notice is marked`);
    eq(renderOrder(entries).map((i) => feed[i].seq), [7, 13, 8, 9, 10, 11, 12], `${name}: render order`);
  }

  // After its request's own replies, before the next message.
  const withReply = items([chat(1, "go"), agent(2, "jack", "codex", 1, "working on it"), chat(3, "and this"), agent(4, "jack", "codex", 1, "The agent stopped responding.", "job-failed")], true);
  eq(shape(withReply, layoutTurns(withReply)), [1, 2, 4, 3], "after the request's replies");

  // A notice that lands before any later message stays where it is, unnamed.
  const onTime = items([chat(1, "go"), agent(2, "jack", "codex", 1, "The agent stopped responding.", "job-failed"), chat(3, "next")], true);
  const onTimeEntries = layoutTurns(onTime);
  eq(shape(onTime, onTimeEntries), [1, 2, 3], "on time: in place");
  assert(onTimeEntries.every((e) => e.kind !== "row" || e.about === undefined), "on time: not named");

  // A notice whose request is not loaded stays in place.
  const orphan = items([chat(3, "next"), agent(4, "jack", "codex", 1, "The agent stopped responding.", "job-failed")], true);
  eq(shape(orphan, layoutTurns(orphan)), [3, 4], "request not loaded: in place");

  // A reply (no notice) that lands late keeps its place: only notices move.
  const lateReply = items([chat(1, "go"), chat(2, "next"), agent(3, "jack", "codex", 1, "done")], true);
  eq(shape(lateReply, layoutTurns(lateReply)), [1, 2, 3], "a late reply stays");

  // A request with delegated work keeps its notices in its block.
  const delegated = items([chat(1, "go"), dispatch(2, 1, ["bob", "claude"]), chat(3, "next"), agent(4, "jack", "codex", 1, "The agent stopped responding.", "job-failed")], true);
  eq(shape(delegated, layoutTurns(delegated)), [1, [1, [[2, 0], [4, 0]]], 3], "a delegated request's notice is in its block");

  // The "New" divider skips a late notice (it sits among read rows).
  const unread = [...liveQa, ...items([chat(14, "are you online?")], true)];
  const unreadEntries = layoutTurns(unread);
  const seqOf = (i: number) => unread[i].seq ?? 0;
  eq(unread[unreadDividerAt(renderOrder(unreadEntries), seqOf, 13)]?.seq, 14, "the divider is above #14, not the late notice");
  eq(unread[unreadDividerAt(renderOrder(unreadEntries), seqOf, 10)]?.seq, 10, "an earlier baseline");
  eq(unreadDividerAt(renderOrder(unreadEntries), seqOf, 15), -1, "nothing unread");
  eq(unreadDividerAt(renderOrder(unreadEntries), seqOf, null), -1, "no baseline");
  // Inside a block: the first unread row on screen.
  eq(live[unreadDividerAt(renderOrder(layoutTurns(live)), (i) => live[i].seq ?? 0, 13)]?.seq, 13, "a block's row");

  eq(aboutRequest("hello, please read the attached file and tell me how many lines it has"), "About: hello, please read the attached file and tell me how many l…", "about: the request's first words");
  eq(aboutRequest("\n\nshort ask\nsecond line"), "About: short ask", "about: the first line");
  eq(aboutRequest(""), "About an earlier message", "about: a request without text");
}
