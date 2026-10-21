// The room feed's two layouts, built from the same rows: By time
// (layoutTurns) and By request (layoutRequests,
// docs/prd/2026-10-21-feed-by-request.md). A member picks one
// (useFeedLayout); the unread divider and the render order serve both.
//
// Delegated work (docs/prd/2026-10-12-experts-and-teams.md, "Runtime gap
// closure" 1). An agent that dispatches work through the room tools (a team
// lead asking its roles, or any ask_agent) produces a subtree of rows: each
// dispatch (a chat message the requester "sent" through the agent), the
// dispatched agents' replies and notices, nested dispatches, and the lead's
// progress lines. By time shows that subtree as one collapsible block right
// under the request that started it, then the lead's own replies:
//
//   request → [Team: dispatch, its replies, … , progress lines] → lead's answer
//
// The grouping reads only the payload chain, never the job list, so a live
// feed and one rebuilt from history group the same way:
//   - a dispatch's parent request is dispatch.parentSeq;
//   - an agent row's request is its sourceSeq (payload.sourceSeq).
// A row whose parent request is not loaded (an older history page) stays in
// place until "Load earlier" brings the request in.
//
// A job's outcome notice (a failure, a refusal, attachments that did not
// arrive) is about its request (payload.sourceSeq), not about whatever was
// posted last. By time, one that lands after a later message (a queued job
// that failed minutes later, while the member had moved on) renders at the
// end of its request's turn, before that later message, and names the
// request (aboutRequest), so it never reads as news about the newest
// request (docs/bugs/2026-10-21-late-failure-notice-and-ownerless-
// recipient.md). The lines an agent posts while it works (progress, a
// setup change, a publish card, a context reset) and its replies stay in
// arrival order: By time means time. By request, every row is in its
// request's block.
import type { DispatchAttribution } from "../../lib/dispatch.ts";
import { isLiveJob } from "../../lib/agentRuns.ts";
import { turnStart, type JobProgress } from "../../lib/jobProgress.ts";
import { titleFromText } from "../../lib/promptPacks.ts";
import { AgentJobTerminalStatuses, NoticeContextReset, NoticeJobFailed, NoticeProgress, NoticePublishProposal, NoticeRoomSetup, type AgentJob } from "../../lib/wire.gen.ts";

// The fields of a feed row the layouts read (FeedItem satisfies it).
export interface TurnRow {
  readonly seq?: number;
  readonly author: string;
  readonly dispatch?: DispatchAttribution;
  readonly sourceSeq?: number;
  readonly notice?: string;
  readonly agentLabel?: string;
  readonly text?: string;
  readonly createdAtMs?: number;
  readonly peopleOnly?: boolean;
}

// One nested row of a block: its feed index and indent level (0 = a
// dispatch of the request itself or the lead's notice line).
export interface TurnBlockRow {
  readonly index: number;
  readonly level: number;
}

// The delegated work under one request.
export interface TurnBlock {
  // The request row's feed index.
  readonly anchor: number;
  readonly rows: readonly TurnBlockRow[];
  // Feed indices of the dispatch rows (their jobs give the block's status).
  readonly dispatches: readonly number[];
  // Team roles staffed by the dispatches, in order, without repeats; empty
  // for plain delegation.
  readonly roles: readonly string[];
  // The addressed agents' handles, in order, without repeats.
  readonly agents: readonly string[];
}

// A step inside a request's block: one row, or the request's delegated work.
export type RequestStep = { readonly kind: "row"; readonly index: number } | { readonly kind: "block"; readonly block: TurnBlock };

// One request By request: the request row, its steps (collapsed) and its
// foot (always shown).
export interface RequestBlock {
  // The request row's feed index.
  readonly anchor: number;
  // The agent the request went to, as a handle ("" until a row names it).
  readonly agent: string;
  // Progress lines, the delegated work and every reply batch before the
  // last, in feed order.
  readonly steps: readonly RequestStep[];
  // Every row inside the steps, in render order (the step count, the jump
  // target, the latest step).
  readonly stepRows: readonly number[];
  // The answer (the last reply batch) and every other notice (a failure, a
  // timeout, a setup change, a publish card), in feed order.
  readonly foot: readonly number[];
}

// The feed in render order: plain rows and, By time, right after a request
// with delegated work, its block; By request, a block per request. A late
// notice's row (By time) carries `about`: the feed index of the request it
// was moved back to.
export type FeedEntry =
  | { readonly kind: "row"; readonly index: number; readonly about?: number }
  | { readonly kind: "block"; readonly block: TurnBlock }
  | { readonly kind: "request"; readonly request: RequestBlock };

const isAgentRow = (r: Pick<TurnRow, "author">) => r.author === "agent";
const seqOf = (r: TurnRow) => r.seq ?? 0;

// The payload chain, read once per layout.
interface Chain {
  // parent[i] = feed index of the request row i belongs under, or -1.
  readonly parent: readonly number[];
  readonly children: ReadonlyMap<number, readonly number[]>;
  readonly isDispatch: (i: number) => boolean;
}

function readChain(feed: readonly TurnRow[]): Chain {
  // Requests (people's messages, dispatches included) by room sequence.
  const requestAt = new Map<number, number>();
  feed.forEach((r, i) => {
    const s = seqOf(r);
    if (s > 0 && !isAgentRow(r) && !requestAt.has(s)) requestAt.set(s, i);
  });
  const parent = feed.map((r, i) => {
    const up = isAgentRow(r) ? r.sourceSeq : r.dispatch?.parentSeq;
    if (typeof up !== "number" || up <= 0) return -1;
    const at = requestAt.get(up);
    return at === undefined || at === i ? -1 : at;
  });
  const children = new Map<number, number[]>();
  parent.forEach((p, i) => {
    if (p < 0) return;
    const list = children.get(p);
    if (list) list.push(i);
    else children.set(p, [i]);
  });
  return { parent, children, isDispatch: (i) => !isAgentRow(feed[i]) && parent[i] >= 0 };
}

// delegatedBlock is a root request's delegated work: its dispatches (each
// with its replies, notices and nested dispatches, depth-first) and the
// request's own notice lines that `joins` admits; null when nothing was
// delegated. Every row it takes is added to `nested`.
function delegatedBlock(feed: readonly TurnRow[], chain: Chain, anchor: number, joins: (i: number) => boolean, nested: Set<number>): TurnBlock | null {
  const { children, isDispatch } = chain;
  const direct = children.get(anchor) ?? [];
  if (!direct.some(isDispatch)) return null; // nothing delegated: rows stay flat
  const rows: TurnBlockRow[] = [];
  const dispatches: number[] = [];
  const roles: string[] = [];
  const agents: string[] = [];
  const visit = (i: number, level: number, seen: Set<number>) => {
    if (seen.has(i)) return; // a malformed chain never loops
    seen.add(i);
    rows.push({ index: i, level });
    nested.add(i);
    if (isDispatch(i)) {
      dispatches.push(i);
      const d = feed[i].dispatch;
      if (d?.role && !roles.includes(d.role)) roles.push(d.role);
      if (d?.to && !agents.includes(d.to)) agents.push(d.to);
      for (const c of children.get(i) ?? []) visit(c, level + 1, seen);
    }
  };
  const seen = new Set<number>([anchor]);
  for (const c of direct) if (isDispatch(c) || (isAgentRow(feed[c]) && joins(c))) visit(c, 0, seen);
  return { anchor, rows, dispatches, roles, agents };
}

// The notices an agent posts while its job works: they belong at the time
// they were posted. Every other notice reports how the job ended (the
// server's job-failed, the bridge's refusals, a member timeout,
// attachments or a file edit that did not arrive), so a new kind of
// failure is placed with its request by default.
const IN_TURN_NOTICES: ReadonlySet<string> = new Set([NoticeProgress, NoticeRoomSetup, NoticePublishProposal, NoticeContextReset]);

// reportsOutcome: the row is a notice about how its job ended.
const reportsOutcome = (r: Pick<TurnRow, "notice">) => !!r.notice && !IN_TURN_NOTICES.has(r.notice);

const isRoot = (feed: readonly TurnRow[], chain: Chain, i: number) => chain.parent[i] < 0 && !isAgentRow(feed[i]);

// layoutTurns is By time: rows in arrival order, delegated work grouped
// under its request. Rows keep their feed order inside a block,
// depth-first per dispatch: a dispatch, then its replies, notices and
// nested dispatches, each indented by its depth.
export function layoutTurns(feed: readonly TurnRow[]): FeedEntry[] {
  const chain = readChain(feed);
  const { parent } = chain;
  const nested = new Set<number>();
  const blocks = new Map<number, TurnBlock>();
  feed.forEach((_, anchor) => {
    if (!isRoot(feed, chain, anchor)) return;
    // The request's own dispatches and notice lines go in the block; the
    // lead's replies stay in the feed after it.
    const block = delegatedBlock(feed, chain, anchor, (c) => !!feed[c].notice, nested);
    if (block) blocks.set(anchor, block);
  });

  // Late notices: a top-level outcome notice whose request is loaded and was
  // followed by another top-level message before the notice landed moves
  // to just before that message (the end of its request's turn).
  const late = new Map<number, number>(); // notice index → its request's index
  const lateBefore = new Map<number, number[]>(); // message index → notices placed before it
  feed.forEach((r, i) => {
    const req = parent[i];
    if (!isAgentRow(r) || !reportsOutcome(r) || req < 0 || nested.has(i)) return;
    for (let k = req + 1; k < i; k++) {
      if (isAgentRow(feed[k]) || nested.has(k)) continue;
      late.set(i, req);
      const list = lateBefore.get(k);
      if (list) list.push(i);
      else lateBefore.set(k, [i]);
      return;
    }
  });

  const out: FeedEntry[] = [];
  feed.forEach((_, i) => {
    if (nested.has(i) || late.has(i)) return;
    for (const n of lateBefore.get(i) ?? []) out.push({ kind: "row", index: n, about: late.get(n) });
    out.push({ kind: "row", index: i });
    const block = blocks.get(i);
    if (block) out.push({ kind: "block", block });
  });
  return out;
}

// layoutRequests is By request: a block per request, in the order the
// requests were made, each holding its whole turn wherever its rows landed
// in time; rows that answer no request stay in time order between blocks.
//   - A request is a root person's row addressed to an agent (not
//     people-only; a scheduled or event-triggered post is its owner's row),
//     or one that rows answer anyway. A dispatch whose request is loaded is
//     part of that request's delegated work, else a request of its own.
//   - Steps: progress lines, the delegated work, reply batches before the
//     last. Foot: the last reply batch (one agent message can carry
//     several parts, sharing its sequence) and every other notice.
export function layoutRequests(feed: readonly TurnRow[]): FeedEntry[] {
  const chain = readChain(feed);
  const { children } = chain;
  const taken = new Set<number>();
  const requests = new Map<number, RequestBlock>();
  feed.forEach((r, anchor) => {
    if (!isRoot(feed, chain, anchor)) return;
    const direct = children.get(anchor) ?? [];
    if (r.peopleOnly && direct.length === 0) return; // people talking: a plain row
    // The delegated work holds the dispatches only: the request's own
    // progress lines are steps beside it, in time order.
    const inBlock = new Set<number>();
    const block = delegatedBlock(feed, chain, anchor, () => false, inBlock);
    const replies = direct.filter((c) => isAgentRow(feed[c]) && !feed[c].notice);
    const answerSeq = replies.length > 0 ? seqOf(feed[replies[replies.length - 1]]) : -1;
    const steps: RequestStep[] = [];
    const foot: number[] = [];
    let blockPlaced = false;
    for (const c of direct) {
      if (inBlock.has(c)) {
        if (block && !blockPlaced) steps.push({ kind: "block", block });
        blockPlaced = true;
        continue;
      }
      const row = feed[c];
      if (!isAgentRow(row)) continue; // a dispatch is always in the block
      if (row.notice === NoticeProgress) steps.push({ kind: "row", index: c });
      else if (row.notice) foot.push(c);
      else if (seqOf(row) === answerSeq) foot.push(c);
      else steps.push({ kind: "row", index: c });
    }
    const stepRows = steps.flatMap((s) => (s.kind === "row" ? [s.index] : s.block.rows.map((b) => b.index)));
    const firstAgent = direct.find((c) => isAgentRow(feed[c]) && feed[c].agentLabel);
    const agent = r.dispatch?.to || (firstAgent === undefined ? "" : feed[firstAgent].agentLabel) || "";
    for (const i of [...inBlock, ...direct]) taken.add(i);
    requests.set(anchor, { anchor, agent, steps, stepRows, foot });
  });
  const out: FeedEntry[] = [];
  feed.forEach((_, i) => {
    const request = requests.get(i);
    if (request) out.push({ kind: "request", request });
    else if (!taken.has(i)) out.push({ kind: "row", index: i });
  });
  return out;
}

// The longest request excerpt a late notice shows.
const ABOUT_MAX = 60;

// aboutRequest names the request a late notice is about: "About: <its
// first line>", shortened; a request with no text (attachments only) is
// "About an earlier message".
export function aboutRequest(text: string): string {
  const title = titleFromText(text, ABOUT_MAX);
  return title ? `About: ${title}` : "About an earlier message";
}

// renderOrder flattens a layout into the order rows appear on screen (By
// time blocks expanded), for the unread divider and sender grouping. A
// request block is its request, its steps (left out with steps: false,
// for what a member sees with the steps folded) and its foot.
export function renderOrder(entries: readonly FeedEntry[], opts: { readonly steps?: boolean } = {}): number[] {
  const out: number[] = [];
  for (const e of entries) {
    if (e.kind === "row") out.push(e.index);
    else if (e.kind === "block") for (const r of e.block.rows) out.push(r.index);
    else out.push(e.request.anchor, ...(opts.steps === false ? [] : e.request.stepRows), ...e.request.foot);
  }
  return out;
}

// renderRuns splits the render order into the runs rows group by sender
// within (sameSender): By time the whole feed is one run; a request block
// is three (its request, its steps, its foot), so an answer never hides its
// agent's name behind a folded step, and the rows between blocks run on.
export function renderRuns(entries: readonly FeedEntry[]): number[][] {
  const runs: number[][] = [];
  let loose: number[] = [];
  for (const e of entries) {
    if (e.kind !== "request") {
      loose.push(...renderOrder([e]));
      continue;
    }
    if (loose.length > 0) runs.push(loose);
    loose = [];
    runs.push([e.request.anchor], [...e.request.stepRows], [...e.request.foot]);
  }
  if (loose.length > 0) runs.push(loose);
  return runs;
}

// unreadDividerAt is the feed index the "New" divider sits above, given the
// rows in screen order: the first row of the unread run (rows at or past
// the read baseline) that ends the feed. A new row shown above a read row
// (a late notice back at its request; a reply in an earlier request's
// block) would split the read part, so it never starts the run. Rows
// without a sequence yet (a send not acknowledged) are neutral. -1: no
// divider.
export function unreadDividerAt(order: readonly number[], seqOf: (index: number) => number, unreadFromSeq: number | null): number {
  if (unreadFromSeq === null) return -1;
  let at = -1;
  for (const i of order) {
    const s = seqOf(i);
    if (s <= 0) continue;
    if (s < unreadFromSeq) at = -1;
    else if (at < 0) at = i;
  }
  return at;
}

// blockTitle is the block's header: "Team · writer, critic" for a team's
// roles, else "Delegated to bob/claude" (or "to 3 agents").
export function blockTitle(block: Pick<TurnBlock, "roles" | "agents" | "dispatches">): string {
  if (block.roles.length > 0) return `Team · ${block.roles.join(", ")}`;
  if (block.agents.length === 1) return `Delegated to ${block.agents[0]}`;
  if (block.agents.length > 1) return `Delegated to ${block.agents.length} agents`;
  return block.dispatches.length === 1 ? "Delegated work" : `Delegated work (${block.dispatches.length})`;
}

export interface BlockProgress {
  readonly done: number;
  readonly total: number;
  // Some dispatched job is still working (the block stays open).
  readonly active: boolean;
}

// blockProgress summarizes the dispatched jobs: how many settled and whether
// any is still working. statusOf answers a dispatch row's job status, or
// undefined when the room's job list does not hold it (an old turn): such a
// job counts neither as working nor as done.
export function blockProgress(block: Pick<TurnBlock, "dispatches">, statusOf: (index: number) => string | undefined): BlockProgress {
  let done = 0;
  let active = false;
  for (const i of block.dispatches) {
    const s = statusOf(i);
    if (!s) continue;
    if (isLiveJob({ status: s })) active = true;
    else done++;
  }
  return { done, total: block.dispatches.length, active };
}

// progressText is the block header's state: "1 of 3 done" while working,
// "Done" / "All 3 done" once settled, else how many tasks it holds.
export function progressText(p: BlockProgress): string {
  if (p.active) return `${p.done} of ${p.total} done`;
  if (p.done === p.total) return p.total === 1 ? "Done" : `All ${p.total} done`;
  return p.total === 1 ? "1 task" : `${p.total} tasks`;
}

// A request's state in its block's head: working, failed, answered, or
// unknown (an old turn the room's job list no longer holds, unanswered).
export type RequestState = "live" | "failed" | "done" | "none";

type HeadJob = Pick<AgentJob, "status" | "createdAtMs" | "claimedAtMs" | "startedAtMs" | "finishedAtMs">;

// A job that ended without a reply (failed, timed out, cancelled): the
// terminal statuses (store.AgentJobStatus.Terminal) but replied.
const endedUnanswered = (j: Pick<AgentJob, "status">) => j.status !== "replied" && (AgentJobTerminalStatuses as readonly string[]).includes(j.status);

// requestState decides the head's state from the request's jobs (the room's
// job list) and its foot: any job still working is live; a settled job
// failed (and none replied), or, with no job known, a job-failed notice,
// is failed; a reply (a replied job, or an answer row) is done.
export function requestState(jobs: readonly Pick<AgentJob, "status">[], foot: readonly Pick<TurnRow, "author" | "notice">[]): RequestState {
  if (jobs.some((j) => isLiveJob(j))) return "live";
  if (jobs.some((j) => j.status === "replied")) return "done";
  if (jobs.some(endedUnanswered)) return "failed";
  if (foot.some((r) => r.notice === NoticeJobFailed)) return "failed";
  return foot.some((r) => isAgentRow(r) && !r.notice) ? "done" : "none";
}

// requestElapsed is the head's time in ms: while live, since the turn
// started; once settled, from start to finish (the job's times, else the
// request's and the answer's timestamps); null when unknown.
export function requestElapsed(
  state: RequestState,
  job: HeadJob | undefined,
  progress: JobProgress | undefined,
  rows: { readonly askedAtMs?: number; readonly answeredAtMs?: number },
  nowMs: number,
): number | null {
  if (state === "live" && job) return Math.max(0, nowMs - turnStart(job, progress));
  if (state === "none") return null;
  if (job?.finishedAtMs) return Math.max(0, job.finishedAtMs - turnStart(job));
  if (rows.askedAtMs && rows.answeredAtMs) return Math.max(0, rows.answeredAtMs - rows.askedAtMs);
  return null;
}

// The longest step line a head shows.
const STEP_MAX = 140;

// latestStep is the head's latest step while the request is live: the
// newer of the turn's last step row (a progress line, an earlier reply, a
// delegated task) and the job's current progress step, both the agent's own
// words; "" when there is neither.
export function latestStep(
  row: Pick<TurnRow, "text" | "createdAtMs"> | undefined,
  live: { readonly label: string; readonly atMs: number } | undefined,
): string {
  const rowText = row ? titleFromText(row.text ?? "", STEP_MAX) : "";
  if (live && (!rowText || live.atMs >= (row?.createdAtMs ?? 0))) return titleFromText(live.label, STEP_MAX);
  return rowText;
}

// stepCountText is the head's count: "1 step", "4 steps"; "" for none.
export function stepCountText(n: number): string {
  return n === 0 ? "" : n === 1 ? "1 step" : `${n} steps`;
}
