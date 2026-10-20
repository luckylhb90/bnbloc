// Never a dead end (docs/prd/2026-10-20-never-a-dead-end.md): when an
// agent's reply leaves the member's request undone, the room proposes what
// would get it done. This is the one owner of the closed action list and of
// reading the find-a-way assist's reply against what the member can act on
// (a target that is not there is dropped), plus the fixed fallback (no
// assist model) and the once-per-reply memo. Pure: the requests are
// lib/assist.ts and lib/api.ts, the card views/room/DeadEndCard.tsx.

import { AssistTaskFindAWay } from "./wire.gen.ts";

/** The assist that proposes the fixes (resolved by name; its output contract is wire.AssistContracts). */
export const FIND_A_WAY = AssistTaskFindAWay;

/** The closed list of actions a fix may take: each is wired to an existing
 *  flow by the card, nothing else is ever offered. */
export const DEAD_END_ACTIONS = ["install", "ask-agent", "attach", "own-agent", "rephrase", "report"] as const;
export type DeadEndAction = (typeof DEAD_END_ACTIONS)[number];

/** What stopped a request (the assist's `cause`); anything else is "other". */
export const DEAD_END_CAUSES = ["access", "missing-file", "capability", "unclear", "agent-error", "other"] as const;
export type DeadEndCause = (typeof DEAD_END_CAUSES)[number];

/** At most this many fixes are shown. */
export const MAX_FIXES = 3;

/** Bounds of the texts a fix carries (a hint, a note to admins, a why). */
const MAX_HINT = 200;
const MAX_NOTE = 1000;
const MAX_WHY = 300;
const MAX_SUMMARY = 400;

/** One proposed fix: an action, its target (a component name, an agent
 *  handle, a file hint, a note to admins; "" when the action takes none)
 *  and one sentence on how it helps. */
export interface DeadEndFix {
  readonly action: DeadEndAction;
  readonly target: string;
  readonly why: string;
}

/** A proposal for one reply. */
export interface DeadEndProposal {
  readonly cause: DeadEndCause;
  readonly summary: string;
  readonly fixes: readonly DeadEndFix[];
}

/** What the member can act on here: the components they could install
 *  (listed to them, not installed), the agents they can ask in the room
 *  (agentHandle form) and the agent that replied (never re-asked). */
export interface DeadEndScope {
  readonly installable: readonly string[];
  readonly agents: readonly string[];
  readonly replier?: string;
}

function text(v: unknown, max: number): string {
  if (typeof v !== "string") return "";
  const s = v.trim();
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

/** fixTarget is a fix's target when the member can act on it ("" for an
 *  action that takes none), null when the fix is dropped. */
function fixTarget(action: DeadEndAction, raw: unknown, scope: DeadEndScope): string | null {
  const target = text(raw, MAX_NOTE);
  switch (action) {
    case "install":
      return scope.installable.includes(target) ? target : null;
    case "ask-agent":
      return target !== scope.replier && scope.agents.includes(target) ? target : null;
    case "attach":
      return text(target, MAX_HINT);
    case "report":
      return target;
    case "own-agent":
    case "rephrase":
      return "";
  }
}

/** parseProposal reads the find-a-way assist's reply fields: the cause
 *  (unknown: "other"), the summary (none: no proposal) and at most
 *  MAX_FIXES fixes of the closed list whose target is in scope, each fix
 *  once. A reply naming an unknown action, component or agent loses that
 *  fix, never the rest. */
export function parseProposal(fields: Readonly<Record<string, unknown>>, scope: DeadEndScope): DeadEndProposal | null {
  const summary = text(fields.summary, MAX_SUMMARY);
  if (!summary) return null;
  const cause = (DEAD_END_CAUSES as readonly string[]).includes(fields.cause as string) ? (fields.cause as DeadEndCause) : "other";
  const fixes: DeadEndFix[] = [];
  const seen = new Set<string>();
  for (const raw of Array.isArray(fields.fixes) ? fields.fixes : []) {
    if (fixes.length === MAX_FIXES) break;
    if (!raw || typeof raw !== "object") continue;
    const f = raw as Record<string, unknown>;
    const action = f.action as DeadEndAction;
    if (!(DEAD_END_ACTIONS as readonly string[]).includes(action)) continue;
    const target = fixTarget(action, f.target, scope);
    const key = `${action}\u0000${target}`;
    if (target === null || seen.has(key)) continue;
    seen.add(key);
    fixes.push({ action, target, why: text(f.why, MAX_WHY) });
  }
  return { cause, summary, fixes };
}

/** fallbackFixes is what the menu offers with no assist model (or nothing
 *  the assist proposed fits): attach a file, ask another agent of the
 *  room, say the request another way. */
export function fallbackFixes(scope: DeadEndScope): DeadEndFix[] {
  const other = scope.agents.find((a) => a !== scope.replier);
  return [
    { action: "attach", target: "", why: "" },
    ...(other ? [{ action: "ask-agent" as const, target: other, why: "" }] : []),
    { action: "rephrase", target: "", why: "" },
  ];
}

/** A reply's outcome, remembered so a card is never computed twice for the
 *  same reply: the proposal, or the gate saying there is nothing to fix. */
export type DeadEndOutcome = { readonly kind: "proposal"; readonly proposal: DeadEndProposal } | { readonly kind: "skipped" };

/** How many replies the memo keeps (the oldest is forgotten first). */
const MEMO_SIZE = 500;

/** createReplyMemo remembers outcomes by reply (room and sequence). */
export function createReplyMemo(size = MEMO_SIZE) {
  const outcomes = new Map<string, DeadEndOutcome>();
  const key = (roomId: string, seq: number) => `${roomId}\u0000${seq}`;
  return {
    get: (roomId: string, seq: number): DeadEndOutcome | undefined => outcomes.get(key(roomId, seq)),
    has: (roomId: string, seq: number): boolean => outcomes.has(key(roomId, seq)),
    set(roomId: string, seq: number, outcome: DeadEndOutcome): void {
      outcomes.delete(key(roomId, seq));
      outcomes.set(key(roomId, seq), outcome);
      if (outcomes.size > size) outcomes.delete(outcomes.keys().next().value as string);
    },
  };
}

/** The session's memo: a remount of the room never asks again. */
export const deadEndMemo = createReplyMemo();

/** A job event that settles a turn the member asked for themselves (not one
 *  an agent dispatched for them), in the channel they are looking at. */
export interface SettledJob {
  readonly jobId: string;
  readonly status: string;
  readonly requesterUserId: string;
  readonly channelId?: string;
  readonly dispatchedByJobId?: string;
}

/** ownSettledTurn reports a job update that settles the member's own turn
 *  with a reply in channelId (the moment the gated call runs). */
export function ownSettledTurn(job: SettledJob, subject: string | null, channelId: string | null): boolean {
  return job.status === "replied" && !!subject && job.requesterUserId === subject && !job.dispatchedByJobId && (!job.channelId || job.channelId === channelId);
}

/** A feed row as the card reads it (views/room/types.ts FeedItem). */
export interface DeadEndRow {
  readonly author: string;
  readonly text: string;
  readonly self: boolean;
  readonly seq?: number;
  readonly jobId?: string;
  readonly sourceSeq?: number;
  readonly notice?: string;
}

/** isAgentReply: an agent's stored reply with text (not a notice line). */
export function isAgentReply(row: DeadEndRow): row is DeadEndRow & { readonly seq: number } {
  return row.author === "agent" && !row.notice && row.seq != null && !!row.text.trim();
}

/** lastReplyOf is a job's last stored reply in the feed (the card goes
 *  under it), undefined until it is there. */
export function lastReplyOf<R extends DeadEndRow>(feed: readonly R[], jobId: string): (R & { readonly seq: number }) | undefined {
  for (let i = feed.length - 1; i >= 0; i--) {
    const row = feed[i];
    if (row.jobId === jobId && isAgentReply(row)) return row;
  }
  return undefined;
}

/** requestOf is the member's own request a reply answers (its sourceSeq),
 *  undefined when it is not loaded or not theirs. */
export function requestOf<R extends DeadEndRow>(feed: readonly R[], reply: DeadEndRow): R | undefined {
  if (!reply.sourceSeq) return undefined;
  return feed.find((row) => row.self && row.seq === reply.sourceSeq);
}

/** The label of a fix's button. An agent is named by the card (by display
 *  name, never the raw handle), after "Ask". */
export function fixLabel(fix: DeadEndFix): string {
  switch (fix.action) {
    case "install":
      return `Install ${fix.target}`;
    case "ask-agent":
      return "Ask";
    case "attach":
      return fix.target ? `Attach ${fix.target}` : "Attach a file";
    case "own-agent":
      return "Use your own agent";
    case "rephrase":
      return "Say it another way";
    case "report":
      return "Tell your admins";
  }
}
