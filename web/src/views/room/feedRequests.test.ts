// Both feed layouts from one room (docs/prd/2026-10-21-feed-by-request.md):
// the end-to-end test room of 2026-10-21, with neutral names. Two agents
// ran at once: a scheduled, event-triggered run on maya's claude-code
// (which delegated one check to bob/claude), and maya's request to her
// opencode; jack and maya talked in between; a request maya sent earlier
// failed late, after the others.
import { normalizeRoomEvent, type RoomEvent } from "../../lib/roomEvents.ts";
import type { FeedItem } from "./types.ts";
import { applyIncoming, feedItemFromEvent, mergeHistory } from "./feedModel.ts";
import {
  aboutRequest,
  latestStep,
  layoutRequests,
  layoutTurns,
  renderOrder,
  renderRuns,
  requestElapsed,
  requestState,
  stepCountText,
  unreadDividerAt,
  type FeedEntry,
} from "./feedTurns.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const me = "maya";
const T0 = 1_700_000_000_000;
const at = (seq: number) => T0 + seq * 60_000;

type Agent = readonly [owner: string, provider: string];
const CC: Agent = ["maya", "claude-code"];
const OC: Agent = ["maya", "opencode"];
const BOB: Agent = ["bob", "claude"];

const chat = (seq: number, author: string, text: string, to: Agent | null, extra: Record<string, unknown> = {}): RoomEvent => ({
  kind: "chat_message",
  sequence: seq,
  payload: { userId: author, text, atUnixMs: at(seq), recipient: to ? { ownerId: to[0], provider: to[1] } : { kind: "room" }, ...extra },
});
const reply = (seq: number, who: Agent, sourceSeq: number, text: string, notice?: string): RoomEvent => ({
  kind: "agent_messages",
  sequence: seq,
  atUnixMs: at(seq),
  payload: {
    messages: [
      {
        payload: { role: "assistant", text, ownerUserId: who[0], provider: who[1], jobId: `job_${sourceSeq}`, sourceSeq, ...(notice ? { notice } : {}) },
      },
    ],
  },
});
const step = (seq: number, who: Agent, sourceSeq: number, text: string) => reply(seq, who, sourceSeq, text, "progress");

const room: RoomEvent[] = [
  chat(1, me, "Read the attached file and count its lines.", OC),
  chat(2, me, "A component was published. Run the end-to-end test plan.", CC, { scheduled: { scheduleId: "sch_e2e" } }),
  chat(3, me, "Sorry, the last run was stopped. Continue from where it stopped.", OC),
  step(4, CC, 2, "A: open QA in the browser"),
  step(5, OC, 3, "F2: open the Library's AI assists"),
  step(6, CC, 2, "A1: the 13 default assists are listed"),
  chat(7, "jack", "Is anyone looking at the publish flow?", null),
  chat(8, "bob", "task: check the upload limit", BOB, { dispatchedBy: { jobId: "job_2", parentSeq: 2, depth: 1, via: "maya/claude-code" } }),
  step(9, OC, 3, "F2: the organization's copy is published"),
  reply(10, BOB, 8, "B2: a 10 MB upload answers 132,732 lines"),
  chat(11, me, "Yes, after this run.", null),
  reply(12, OC, 3, "E2E report: 12 passed, 1 waiting for a person, 0 failed."),
  step(13, CC, 2, "F2: the list still shows the deleted copy"),
  reply(14, CC, 2, "This run: 6 passed, 1 failed (F2 does not refresh after delete)."),
  reply(15, ["", "opencode"], 1, "The opencode agent is offline: no bridge picked up this request.", "job-failed"),
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

const liveFeed = items(room, true).reduce<FeedItem[]>((f, it) => applyIncoming(f, it), []);
const reloaded = mergeHistory(mergeHistory([], items(room.slice(8), false)), items(room.slice(0, 8), false));

// A layout in sequence terms: rows by seq (a late notice as [seq, "about",
// its request's seq]), By time blocks as ["T", anchor, [[seq, level]…]],
// requests as ["R", anchor, agent, steps, foot] (a step block as ["T", …]).
function shape(feed: readonly FeedItem[], entries: readonly FeedEntry[]): unknown[] {
  const s = (i: number) => feed[i].seq ?? 0;
  const block = (b: { anchor: number; rows: readonly { index: number; level: number }[] }) => ["T", s(b.anchor), b.rows.map((r) => [s(r.index), r.level])];
  return entries.map((e) => {
    if (e.kind === "row") return e.about === undefined ? s(e.index) : [s(e.index), "about", s(e.about)];
    if (e.kind === "block") return block(e.block);
    const r = e.request;
    return ["R", s(r.anchor), r.agent, r.steps.map((st) => (st.kind === "row" ? s(st.index) : block(st.block))), r.foot.map(s)];
  });
}

// By time: arrival order. #2's delegated check and its progress lines are
// grouped under it (the delegated block, as before); the late failure #15
// is back at its request #1, naming it; opencode's progress line #9 stays
// after jack's #7 (a line posted while working is placed at its time).
const byTime = [
  1,
  [15, "about", 1],
  2,
  ["T", 2, [[4, 0], [6, 0], [8, 0], [10, 1], [13, 0]]],
  3,
  5,
  7,
  9,
  11,
  12,
  14,
];
// By request: one block per request in request order, each with its whole
// turn; jack's and maya's chat lines between them, in time order.
const byRequest = [
  ["R", 1, "opencode", [], [15]],
  ["R", 2, "maya/claude-code", [4, 6, ["T", 2, [[8, 0], [10, 1]]], 13], [14]],
  ["R", 3, "maya/opencode", [5, 9], [12]],
  7,
  11,
];
for (const [name, feed] of [["live", liveFeed], ["reloaded", reloaded]] as const) {
  eq(shape(feed, layoutTurns(feed)), byTime, `${name}: by time`);
  eq(shape(feed, layoutRequests(feed)), byRequest, `${name}: by request`);
}

const entries = layoutRequests(liveFeed);
const seqs = (order: readonly number[]) => order.map((i) => liveFeed[i].seq);
eq(seqs(renderOrder(entries)), [1, 15, 2, 4, 6, 8, 10, 13, 14, 3, 5, 9, 12, 7, 11], "render order with the steps open");
eq(seqs(renderOrder(entries, { steps: false })), [1, 15, 2, 14, 3, 12, 7, 11], "render order with the steps folded");
eq(
  renderRuns(entries).map(seqs),
  [[1], [], [15], [2], [4, 6, 8, 10, 13], [14], [3], [5, 9], [12], [7, 11]],
  "sender runs: each block's request, steps and foot apart; the chat lines between blocks run on",
);
eq(renderRuns(layoutTurns(liveFeed)).map(seqs), [renderOrder(layoutTurns(liveFeed)).map((i) => liveFeed[i].seq)], "by time: one run");
eq(aboutRequest(liveFeed[0].text), "About: Read the attached file and count its lines.", "the late notice's line By time");

// The head's facts.
{
  const cc = entries[1];
  assert(cc.kind === "request", "#2 is a request");
  if (cc.kind === "request") {
    const r = cc.request;
    eq(stepCountText(r.stepRows.length), "5 steps", "#2's step count (the delegated rows count)");
    const last = liveFeed[r.stepRows[r.stepRows.length - 1]];
    eq(latestStep(last, undefined), "F2: the list still shows the deleted copy", "the last step row");
    eq(latestStep(last, { label: "Run: npm test", atMs: at(13) + 1 }), "Run: npm test", "a newer live step wins");
    eq(latestStep(last, { label: "Run: npm test", atMs: at(12) }), "F2: the list still shows the deleted copy", "an older live step loses");
    eq(latestStep(undefined, { label: "Thinking", atMs: 1 }), "Thinking", "only a live step");
    eq(latestStep(undefined, undefined), "", "nothing yet");
    eq(stepCountText(0), "", "no steps");
    eq(stepCountText(1), "1 step", "one step");
  }
}

// The head's state: from the job list first, then the foot.
{
  const foot = (seq: number) => {
    const e = entries.find((x) => x.kind === "request" && liveFeed[x.request.anchor].seq === seq);
    return e?.kind === "request" ? e.request.foot.map((i) => liveFeed[i]) : [];
  };
  eq(requestState([{ status: "running" }], foot(2)), "live", "a running job is live (whatever the rows say)");
  eq(requestState([{ status: "queued" }], []), "live", "a queued job is live");
  eq(requestState([{ status: "replied" }], foot(2)), "done", "a replied job");
  eq(requestState([{ status: "failed" }, { status: "replied" }], foot(2)), "done", "a retried job that replied");
  eq(requestState([{ status: "timed_out" }], []), "failed", "a timed-out job");
  eq(requestState([], foot(1)), "failed", "no job known, a failure notice");
  eq(requestState([], foot(3)), "done", "no job known, an answer");
  eq(requestState([], []), "none", "no job, no answer (an old unanswered turn)");
}

// The head's time.
{
  const job = { status: "running", createdAtMs: at(2), claimedAtMs: at(2) + 1000, startedAtMs: at(2) + 2000 } as const;
  eq(requestElapsed("live", job, undefined, {}, at(2) + 65_000), 63_000, "live: since the turn started");
  eq(requestElapsed("done", { ...job, status: "replied", finishedAtMs: at(14) }, undefined, {}, at(20)), at(14) - at(2) - 2000, "settled: start to finish");
  eq(requestElapsed("done", undefined, undefined, { askedAtMs: at(3), answeredAtMs: at(12) }, at(20)), 9 * 60_000, "no job: request to answer");
  eq(requestElapsed("done", undefined, undefined, { askedAtMs: at(3) }, at(20)), null, "no job, no answer time");
  eq(requestElapsed("none", undefined, undefined, { askedAtMs: at(3), answeredAtMs: at(12) }, at(20)), null, "unknown state");
}

// The "New" divider in both views: the first row of the unread run that
// ends what is on screen.
{
  const seqOf = (i: number) => liveFeed[i].seq ?? 0;
  const time = renderOrder(layoutTurns(liveFeed));
  const request = renderOrder(layoutRequests(liveFeed), { steps: false });
  eq(liveFeed[unreadDividerAt(time, seqOf, 12)]?.seq, 12, "by time: above #12");
  eq(unreadDividerAt(request, seqOf, 12), -1, "by request: #12, #14 and #15 sit in blocks above the read chat lines #7 and #11: no divider splits the read part");
  eq(unreadDividerAt(request, seqOf, 15), -1, "by request: only the late notice is new, back at its request");
  eq(liveFeed[unreadDividerAt(request, seqOf, 7)]?.seq, 12, "by request: from #7 on, the unread run starts at #3's answer (#12), above #7 and #11");
  eq(liveFeed[unreadDividerAt(request, seqOf, 2)]?.seq, 15, "by request: from #2 on, the run starts at #1's late failure (#15), under its read request");
  // A new request and its answer at the end: the divider is above them.
  const more = [...liveFeed, ...items([chat(16, me, "Now run the smoke test.", OC), reply(17, OC, 16, "Smoke test passed.")], true)];
  const moreOrder = renderOrder(layoutRequests(more), { steps: false });
  eq(more[unreadDividerAt(moreOrder, (i) => more[i].seq ?? 0, 16)]?.seq, 16, "by request: above the new block");
  eq(more[unreadDividerAt(moreOrder, (i) => more[i].seq ?? 0, 17)]?.seq, 17, "by request: above the new answer in the last block");
  // An unacknowledged send is neutral.
  const pending: FeedItem = { id: -1, author: me, text: "typing…", self: true, status: "pending" };
  const withPending = [...more, pending];
  const pendingOrder = renderOrder(layoutRequests(withPending), { steps: false });
  eq(withPending[unreadDividerAt(pendingOrder, (i) => withPending[i].seq ?? 0, 16)]?.seq, 16, "a pending send keeps the divider");
}

// A delegated request whose parent request is not loaded (an older page) is
// a block of its own, addressed to its agent; loading the parent folds it
// into the parent's steps.
{
  const page = mergeHistory([], items(room.slice(7, 10), false)); // #8, #9, #10
  eq(shape(page, layoutRequests(page)), [["R", 8, "bob/claude", [], [10]], 9], "parent not loaded: its own block; a reply without its request stays a row");
}

// People talking only: no blocks at all.
{
  const talk = items([chat(1, "jack", "lunch?", null), chat(2, me, "sure", null)], true);
  eq(shape(talk, layoutRequests(talk)), [1, 2], "people-only lines are rows");
}

// A request just sent: a block with nothing in it yet (no agent named by a
// row); an unacknowledged one too.
{
  const sent = items([chat(1, me, "hello", OC)], true);
  eq(shape(sent, layoutRequests(sent)), [["R", 1, "", [], []]], "a request without rows yet");
  const optimistic: FeedItem[] = [{ id: -2, author: me, text: "hi", self: true, status: "pending" }];
  eq(layoutRequests(optimistic).map((e) => e.kind), ["request"], "an unacknowledged request is a block");
}

// An agent that replied twice in one turn: the earlier reply is a step, the
// last one the answer; a batch of parts sharing one sequence is one answer.
{
  const twice = items(
    [
      chat(1, me, "go", OC),
      reply(2, OC, 1, "Looking at it."),
      {
        kind: "agent_messages",
        sequence: 3,
        payload: {
          messages: [
            { payload: { role: "assistant", text: "Part one.", ownerUserId: "maya", provider: "opencode", jobId: "job_1", sourceSeq: 1 } },
            { payload: { role: "assistant", text: "Part two.", ownerUserId: "maya", provider: "opencode", jobId: "job_1", sourceSeq: 1 } },
          ],
        },
      },
    ],
    true,
  );
  const e = layoutRequests(twice);
  assert(e.length === 1 && e[0].kind === "request", "one block");
  if (e[0].kind === "request") {
    eq(e[0].request.steps.map((st) => (st.kind === "row" ? twice[st.index].text : "block")), ["Looking at it."], "the earlier reply is a step");
    eq(e[0].request.foot.map((i) => twice[i].text), ["Part one.", "Part two."], "the last batch is the answer");
  }
}

// A team's lead failing: its failure notice is at the foot (an outcome is
// never folded), its progress line a step beside the delegated work.
{
  const team = items(
    [
      chat(1, me, "write a haiku", CC),
      step(2, CC, 1, "Phase 1/2: writer"),
      chat(3, "bob", "task", BOB, { dispatchedBy: { jobId: "job_1", parentSeq: 1, depth: 1, via: "maya/claude-code", role: "writer" } }),
      reply(4, CC, 1, "The agent stopped responding.", "job-failed"),
    ],
    true,
  );
  eq(shape(team, layoutRequests(team)), [["R", 1, "maya/claude-code", [2, ["T", 1, [[3, 0]]]], [4]]], "the progress line a step beside the delegated work, the failure at the foot");
  eq(shape(team, layoutTurns(team)), [1, ["T", 1, [[2, 0], [3, 0], [4, 0]]]], "by time keeps every lead notice in the block");
}
