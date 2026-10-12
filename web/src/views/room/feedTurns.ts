// Delegated work in the room feed (docs/prd/2026-10-12-experts-and-teams.md,
// "Runtime gap closure" 1). An agent that dispatches work through the room
// tools (a team lead asking its roles, or any ask_agent) produces a subtree
// of rows: each dispatch (a chat message the requester "sent" through the
// agent), the dispatched agents' replies and notices, nested dispatches, and
// the lead's progress lines. The feed shows that subtree as one collapsible
// block right under the request that started it, then the lead's own replies:
//
//   request → [Team: dispatch, its replies, … , progress lines] → lead's answer
//
// The grouping reads only the payload chain, never the job list, so a live
// feed and one rebuilt from history group the same way:
//   - a dispatch's parent request is dispatch.parentSeq;
//   - an agent row's request is its sourceSeq (payload.sourceSeq).
// A row whose parent request is not loaded (an older history page) stays in
// place until "Load earlier" brings the request in.
import type { DispatchAttribution } from "../../lib/dispatch.ts";

// The fields of a feed row the grouping reads (FeedItem satisfies it).
export interface TurnRow {
  readonly seq?: number;
  readonly author: string;
  readonly dispatch?: DispatchAttribution;
  readonly sourceSeq?: number;
  readonly notice?: string;
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

// The feed in render order: plain rows and, right after a request with
// delegated work, its block.
export type FeedEntry = { readonly kind: "row"; readonly index: number } | { readonly kind: "block"; readonly block: TurnBlock };

const isAgentRow = (r: TurnRow) => r.author === "agent";
const seqOf = (r: TurnRow) => (typeof r.seq === "number" ? Math.floor(r.seq) : 0);

// layoutTurns groups delegated work under its request. Rows keep their feed
// order inside a block, depth-first per dispatch: a dispatch, then its
// replies, notices and nested dispatches, each indented by its depth.
export function layoutTurns(feed: readonly TurnRow[]): FeedEntry[] {
  // Requests (people's messages, dispatches included) by room sequence.
  const requestAt = new Map<number, number>();
  feed.forEach((r, i) => {
    const s = seqOf(r);
    if (s > 0 && !isAgentRow(r) && !requestAt.has(s)) requestAt.set(s, i);
  });
  // parent[i] = feed index of the request row i belongs under, or -1.
  const parent = feed.map((r, i) => {
    const up = isAgentRow(r) ? r.sourceSeq : r.dispatch?.parentSeq;
    if (typeof up !== "number" || up <= 0) return -1;
    const at = requestAt.get(Math.floor(up));
    return at === undefined || at === i ? -1 : at;
  });
  const children = new Map<number, number[]>();
  parent.forEach((p, i) => {
    if (p < 0) return;
    const list = children.get(p);
    if (list) list.push(i);
    else children.set(p, [i]);
  });
  const isDispatch = (i: number) => !isAgentRow(feed[i]) && parent[i] >= 0;

  const nested = new Set<number>();
  const blocks = new Map<number, TurnBlock>();
  feed.forEach((_, anchor) => {
    if (parent[anchor] >= 0 || isAgentRow(feed[anchor])) return; // not a root request
    const direct = children.get(anchor) ?? [];
    if (!direct.some(isDispatch)) return; // nothing delegated: rows stay flat
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
    // The request's own dispatches and notice lines go in the block; the
    // lead's replies (and failure notices) stay in the feed after it.
    for (const c of direct) {
      if (isDispatch(c) || (isAgentRow(feed[c]) && feed[c].notice)) visit(c, 0, seen);
    }
    blocks.set(anchor, { anchor, rows, dispatches, roles, agents });
  });

  const out: FeedEntry[] = [];
  feed.forEach((_, i) => {
    if (nested.has(i)) return;
    out.push({ kind: "row", index: i });
    const block = blocks.get(i);
    if (block) out.push({ kind: "block", block });
  });
  return out;
}

// renderOrder flattens the layout into the order rows appear on screen
// (blocks expanded), for the unread divider and sender grouping.
export function renderOrder(entries: readonly FeedEntry[]): number[] {
  const out: number[] = [];
  for (const e of entries) {
    if (e.kind === "row") out.push(e.index);
    else for (const r of e.block.rows) out.push(r.index);
  }
  return out;
}

// blockTitle is the block's header: "Team · writer, critic" for a team's
// roles, else "Delegated to bob/claude" (or "to 3 agents").
export function blockTitle(block: Pick<TurnBlock, "roles" | "agents" | "dispatches">): string {
  if (block.roles.length > 0) return `Team · ${block.roles.join(", ")}`;
  if (block.agents.length === 1) return `Delegated to ${block.agents[0]}`;
  if (block.agents.length > 1) return `Delegated to ${block.agents.length} agents`;
  return block.dispatches.length === 1 ? "Delegated work" : `Delegated work (${block.dispatches.length})`;
}

// Job statuses that are still working (the block stays open while any is).
const ACTIVE = new Set(["queued", "claimed", "running", "reflowing"]);

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
    if (ACTIVE.has(s)) active = true;
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
