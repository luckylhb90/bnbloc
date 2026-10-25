// Suggest what to use (docs/prd/2026-10-25-suggest-what-to-use.md): after a request
// has been sent to an agent, the one expert, skill pack or MCP tool of the member's
// Library that fits it, or nothing. The organization's decision model answers one
// choice question over the candidates and "none"; this file owns the question, how an
// answer is read (sure enough, clearly ahead), what is not offered again, and the
// member's dismissals. lib/decisions.ts owns the call; the hook
// (views/room/useSentRequestSuggestion.ts) decides when to ask and applies the click;
// the card (views/room/UseSuggestionCard.tsx) draws.
import type { DecisionReply, DecisionRequest, LibraryCandidates } from "./wire.gen.ts";

export type UseKind = "expert" | "skill" | "mcp";

export interface UseCandidate {
  /** The dismissal key: unique per candidate and stable across versions. */
  readonly key: string;
  readonly kind: UseKind;
  /** The content component's id; "" for an MCP tool. */
  readonly id: string;
  readonly name: string;
  readonly label: string;
  readonly description: string;
  readonly tags: readonly string[];
  /** For an expert: the skill packs it wears, by name without the range. */
  readonly wears: readonly string[];
  /** The version a skill pack is turned on at. */
  readonly version?: string;
}

export interface UseSuggestion {
  readonly candidate: UseCandidate;
  /** How sure the decision model was. */
  readonly probability: number;
}

/** candidatesOf reads the server's shortlist (GET /v1/library/candidates) as candidates. */
export function candidatesOf(reply: LibraryCandidates): UseCandidate[] {
  return (reply.candidates ?? []).flatMap((c) => {
    const kind = c.kind === "expert" || c.kind === "skill" || c.kind === "mcp" ? c.kind : null;
    if (!kind) return [];
    return [
      {
        key: kind === "mcp" ? `mcp/${c.name}` : `${kind}/${c.id ?? ""}`,
        kind,
        id: c.id ?? "",
        name: c.name,
        label: c.label || c.name,
        description: c.description ?? "",
        tags: c.tags ?? [],
        wears: c.wears ?? [],
        version: c.version,
      } satisfies UseCandidate,
    ];
  });
}

/** SuggestOptions: what is not offered. */
export interface SuggestOptions {
  /** Candidate keys never to suggest: already picked or on, or dismissed. */
  readonly exclude?: ReadonlySet<string>;
  /** Skill packs (by name) an expert already in effect wears: not suggested. */
  readonly worn?: ReadonlySet<string>;
}

// ---- the organization's decision model places a sent request ----------------
// The same candidates, asked as one choice question of the organization's decision
// model (docs/prd/2026-10-17-decision-models.md): it reads intent and any language,
// and its probabilities are calibrated, so "clear enough" is a threshold, not a
// tuned score. One question per sent request; lib/decisions.ts owns the call.

const ASK_HEAD_CHARS = 800;
const ASK_TAIL_CHARS = 400;
const ASK_DESCRIPTION_CHARS = 160;
/** DECISION_BACKOFF_MS: how long a rate limit or a failure keeps the decision model
 *  out of it (nothing is shown meanwhile). */
export const DECISION_BACKOFF_MS = 60_000;
/** SURE: the least probability a choice needs, and LEAD how many times the
 *  runner-up's (or "none") it must be. */
const SURE = 0.6;
const LEAD = 2;
const NONE = "none";

/** askedText is the part of a request the model reads: all of a short one, the start
 *  (what is wanted) and the end of a long one. */
export function askedText(request: string): string {
  const t = request.trim();
  if (t.length <= ASK_HEAD_CHARS + ASK_TAIL_CHARS) return t;
  return `${t.slice(0, ASK_HEAD_CHARS)}\n[…]\n${t.slice(-ASK_TAIL_CHARS)}`;
}

/** eligibleForAsk are the shortlist's candidates worth asking about: not excluded (picked, on,
 *  dismissed) and no skill pack an expert in effect wears. The server already cut the Library to
 *  a bounded shortlist, so there is nothing to cut here. */
export function eligibleForAsk(candidates: readonly UseCandidate[], options: SuggestOptions = {}): UseCandidate[] {
  return candidates.filter((c) => !options.exclude?.has(c.key) && !(c.kind === "skill" && options.worn?.has(c.name)));
}

const QUESTION =
  "A person is writing a request to an AI agent in a team workspace. Choose the one expert, skill pack or tool from the list that would help most with this request. " +
  'Choose "none" when nothing clearly fits, or the person is only chatting or asking something no listed item is for.';

/** decisionRequest is the one choice question over the eligible candidates and
 *  "none". The request is the input, never part of the instructions. */
export function decisionRequest(request: string, eligible: readonly UseCandidate[]): DecisionRequest {
  const choices = eligible.map((c) => ({ value: c.key, description: `${c.label}. ${c.description.replace(/\s+/g, " ").trim().slice(0, ASK_DESCRIPTION_CHARS)}` }));
  choices.push({ value: NONE, description: "None of these fits." });
  return { input: askedText(request), questions: [{ type: "choice", name: "fit", instructions: QUESTION, choices }] };
}

/** suggestionFromDecision reads the model's answer: the candidate it is sure of
 *  and clearly prefers to every other (and to "none"), or null. An expert takes
 *  the probability of the skill packs it wears, since choosing such a skill pack
 *  means the expert that carries it. */
export function suggestionFromDecision(reply: DecisionReply, eligible: readonly UseCandidate[]): UseSuggestion | null {
  const answer = reply.answers?.[0];
  if (!answer || answer.type !== "choice" || !answer.probabilities) return null;
  const p = new Map<string, number>();
  for (const x of answer.probabilities) p.set(String(x.value), x.probability);
  const stood = new Set(eligible.filter((c) => c.kind === "expert").flatMap((e) => e.wears));
  const entries: { readonly candidate: UseCandidate | null; readonly p: number }[] = [{ candidate: null, p: p.get(NONE) ?? 0 }];
  for (const c of eligible) {
    if (c.kind === "skill" && stood.has(c.name)) continue;
    let prob = p.get(c.key) ?? 0;
    if (c.kind === "expert") for (const s of eligible) if (s.kind === "skill" && c.wears.includes(s.name)) prob += p.get(s.key) ?? 0;
    entries.push({ candidate: c, p: prob });
  }
  entries.sort((a, b) => b.p - a.p);
  const [best, next] = entries;
  if (!best?.candidate || best.p < SURE || (next && best.p < LEAD * next.p)) return null;
  return { candidate: best.candidate, probability: best.p };
}

// ---- the member's choice of how suggestions are placed -----------------------

/** aiSuggestionsOn: whether suggestions are placed by the organization's AI (a request
 *  the member sent is read by it). On unless the member turned it off. */
export function aiSuggestionsOn(storage: Pick<globalThis.Storage, "getItem"> | null, storeKey: string): boolean {
  if (!storage) return true;
  try {
    return storage.getItem(storeKey) !== "off";
  } catch {
    return true;
  }
}

/** setAiSuggestions keeps the member's choice. */
export function setAiSuggestions(storage: Pick<globalThis.Storage, "setItem"> | null, storeKey: string, on: boolean): void {
  try {
    storage?.setItem(storeKey, on ? "on" : "off");
  } catch {
    // private mode: the choice is not kept
  }
}

/** suggestionAction: what the card's button does, in the member's words. */
export function suggestionAction(c: UseCandidate): string {
  return c.kind === "expert" ? "Ask again with it" : c.kind === "skill" ? "Turn on and ask again" : "Add and ask again";
}

/** suggestionText: the card's sentence. */
export function suggestionText(c: UseCandidate): string {
  return `${c.label} fits this request.`;
}

// ---- dismissals: per member, for good (a month) ----------------------------

/** DISMISS_DAYS: how long a dismissed suggestion stays quiet. */
export const DISMISS_DAYS = 30;
const MAX_DISMISSED = 200;
type Store = Pick<globalThis.Storage, "getItem" | "setItem">;

function readDismissed(storage: Store | null, storeKey: string, now: number): Record<string, number> {
  if (!storage) return {};
  try {
    const raw = JSON.parse(storage.getItem(storeKey) ?? "{}") as Record<string, unknown>;
    const out: Record<string, number> = {};
    for (const [k, v] of Object.entries(raw)) if (typeof v === "number" && v > now) out[k] = v;
    return out;
  } catch {
    return {};
  }
}

/** dismissedKeys are the candidate keys the member dismissed and that are still
 *  quiet. storeKey is the member's own storage key (lib/storageKeys). */
export function dismissedKeys(storage: Store | null, storeKey: string, now: number): Set<string> {
  return new Set(Object.keys(readDismissed(storage, storeKey, now)));
}

/** dismissSuggestion keeps a candidate quiet for DISMISS_DAYS. */
export function dismissSuggestion(storage: Store | null, storeKey: string, key: string, now: number): void {
  if (!storage) return;
  const kept = readDismissed(storage, storeKey, now);
  kept[key] = now + DISMISS_DAYS * 86_400_000;
  const entries = Object.entries(kept).sort((a, b) => b[1] - a[1]).slice(0, MAX_DISMISSED);
  try {
    storage.setItem(storeKey, JSON.stringify(Object.fromEntries(entries)));
  } catch {
    // private mode: it may be suggested again
  }
}

// ---- which sent requests earn a question -------------------------------------

/** MIN_REQUEST_CHARS: a shorter message says too little to place ("ok", "thanks"). */
export const MIN_REQUEST_CHARS = 12;

/** A request the member sent, as the hook sees it once the server took it. */
export interface SentRequest {
  readonly text: string;
  /** Sent to an agent (not people only). */
  readonly toAgent: boolean;
  /** The expert it already wore, if any. */
  readonly expertId?: string;
}

/** asksAbout: whether a sent request is worth one decision. Only a message that went
 *  to an agent, long enough to say something, and that did not already pick an expert
 *  (the member has chosen how to ask): a person who picked one needs no suggestion. */
export function asksAbout(r: SentRequest): boolean {
  return r.toAgent && !r.expertId && r.text.trim().length >= MIN_REQUEST_CHARS;
}
