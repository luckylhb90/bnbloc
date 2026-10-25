// Suggest what to use (docs/prd/2026-10-25-suggest-what-to-use.md): from what a
// member is writing, the one expert, skill pack or MCP tool of their Library that
// fits it, or nothing. Pure and local: the draft never leaves the browser, the
// ranking is a keyword score over names, tags and descriptions the client already
// holds. The one owner of the tokenizing, the scoring and the "clear enough to
// show" rules; the hook (views/composer/useUseSuggestion.ts) loads the Library and
// applies the click, the row (views/composer/UseSuggestionRow.tsx) draws.
import type { ContentComponent, DecisionReply, DecisionRequest, MemberComponent } from "./wire.gen.ts";

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
  /** The words of the request that matched, for the row's tooltip (the keyword path). */
  readonly matched: readonly string[];
  /** How sure the decision model was (the decision path). */
  readonly probability?: number;
}

/** The member catalog (GET /v1/components) as candidates: content experts and
 *  skill packs, and the MCP tools the member has not added. */
export function candidatesOf(reply: {
  readonly components?: readonly MemberComponent[];
  readonly contentComponents?: readonly ContentComponent[];
}): UseCandidate[] {
  const out: UseCandidate[] = [];
  for (const c of reply.contentComponents ?? []) {
    if (c.kind !== "expert" && c.kind !== "skill") continue;
    out.push({
      key: `${c.kind}/${c.id}`,
      kind: c.kind,
      id: c.id,
      name: c.name,
      label: c.displayName?.trim() || c.name,
      description: c.description ?? "",
      tags: c.tags ?? [],
      wears: (c.requires?.skills ?? []).map((s) => s.split("@")[0] ?? s),
      version: c.version,
    });
  }
  for (const c of reply.components ?? []) {
    if (c.kind !== "mcp" || c.installed) continue;
    out.push({ key: `mcp/${c.name}`, kind: "mcp", id: "", name: c.name, label: c.name, description: c.description ?? "", tags: [], wears: [] });
  }
  return out;
}

const STOP = new Set(
  (
    "a an the and or of to in on for with from by is are be this that it its as at we you your my our me i can could would should will do does how what which who " +
    "please need want like make made makes build create give get use using show tell about into over than then them they their there here also just more most some any " +
    "all not no yes if so but out up down new one two where ask list item items thing things way work working let see look check"
  ).split(" "),
);
// Words that stand for one another in a request and a description.
const SAME: Record<string, string> = { website: "page", webpage: "page", site: "page", landing: "page", homepage: "page", llm: "ai", chatbot: "assistant", application: "app", webapp: "app" };
// Chinese filler pairs, never a keyword.
const CJK_STOP = new Set("一个 一份 一下 一些 帮我 我们 大家 可以 需要 如何 怎么 这个 那个 请问 帮忙 麻烦 能否 是否 什么 为什么 有没有 的话 然后 以及 或者 还是 已经 一起 让大".split(" "));
const HAN = /\p{Script=Han}/u;

// STEM_CHARS: a word is known by its first characters, so "translate" and
// "translation", "review" and "reviewer", "report" and "reporting" are one word. A
// longer prefix keeps more words apart; measured on a real Library, 6 found more
// of the right ones than 5 or none without showing more wrong ones.
const STEM_CHARS = 6;

function stem(w: string): string {
  const same = SAME[w];
  if (same) return same;
  let out = w;
  if (w.length > 5 && w.endsWith("ing")) out = w.slice(0, -3);
  else if (w.length > 4 && w.endsWith("ies")) out = w.slice(0, -3) + "y";
  else if (w.length > 4 && w.endsWith("ed")) out = w.slice(0, -2);
  else if (w.length > 3 && w.endsWith("s") && !w.endsWith("ss")) out = w.slice(0, -1);
  return out.length > STEM_CHARS ? out.slice(0, STEM_CHARS) : out;
}

/** tokens: the keywords of a text. Words are lower-cased, stemmed and stripped of
 *  filler; a run of Chinese becomes overlapping pairs of characters. */
export function tokens(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.normalize("NFKC").toLowerCase().split(/[^\p{L}\p{N}]+/u)) {
    if (!raw) continue;
    if (HAN.test(raw)) {
      const cs = [...raw];
      if (cs.length === 1) out.push(raw);
      for (let i = 0; i + 1 < cs.length; i++) {
        const pair = (cs[i] ?? "") + (cs[i + 1] ?? "");
        if (!CJK_STOP.has(pair)) out.push(pair);
      }
      continue;
    }
    if (raw.length < 2 || STOP.has(raw)) continue;
    out.push(stem(raw));
  }
  return out;
}

/** MIN_DRAFT_CHARS: a request shorter than this says too little to place. */
export const MIN_DRAFT_CHARS = 12;
/** SHORT_REQUEST: a request of at most this many keywords may be placed by one
 *  word of a candidate's name; a longer one needs two matching words. */
const SHORT_REQUEST = 8;
/** RARE_NAME_WORD: a name word places a request alone only when at most this
 *  many candidates carry it ("postmortem" does; "design" or "team" may not). */
const RARE_NAME_WORD = 2;
/** CLOSE: a runner-up with the same coverage within this factor of the best
 *  score means the request does not tell them apart. */
const CLOSE = 1.15;

interface Indexed {
  readonly c: UseCandidate;
  readonly name: ReadonlySet<string>;
  readonly tags: ReadonlySet<string>;
  readonly desc: ReadonlySet<string>;
}

interface Index {
  readonly docs: readonly Indexed[];
  readonly idf: (t: string) => number;
  readonly nameDf: (t: string) => number;
}

const indexes = new WeakMap<readonly UseCandidate[], Index>();

function indexOf(candidates: readonly UseCandidate[]): Index {
  const cached = indexes.get(candidates);
  if (cached) return cached;
  const docs: Indexed[] = candidates.map((c) => ({
    c,
    name: new Set(tokens(`${c.name} ${c.label}`)),
    tags: new Set(tokens(c.tags.join(" "))),
    desc: new Set(tokens(c.description)),
  }));
  const df = new Map<string, number>();
  const nameDf = new Map<string, number>();
  for (const d of docs) {
    for (const t of new Set([...d.name, ...d.tags, ...d.desc])) df.set(t, (df.get(t) ?? 0) + 1);
    for (const t of d.name) nameDf.set(t, (nameDf.get(t) ?? 0) + 1);
  }
  const n = docs.length;
  const index: Index = { docs, idf: (t) => Math.log(1 + n / (1 + (df.get(t) ?? 0))), nameDf: (t) => nameDf.get(t) ?? 0 };
  indexes.set(candidates, index);
  return index;
}

export interface SuggestOptions {
  /** Candidate keys never to suggest: already picked or on, or dismissed. */
  readonly exclude?: ReadonlySet<string>;
  /** Skill packs (by name) an expert already in effect wears: not suggested. */
  readonly worn?: ReadonlySet<string>;
}

/** suggestUse is the one candidate that fits the request, or null when none does
 *  clearly (docs/prd/2026-10-25-suggest-what-to-use.md, rule 3). Words weigh by how
 *  rare they are across the Library and by where they match: a name over a tag
 *  over a description. */
export function suggestUse(draft: string, candidates: readonly UseCandidate[], options: SuggestOptions = {}): UseSuggestion | null {
  const query = [...new Set(tokens(draft))];
  if (query.length === 0) return null;
  const index = indexOf(candidates);
  interface Hit {
    readonly d: Indexed;
    readonly words: string[];
    readonly score: number;
  }
  let hits: Hit[] = [];
  for (const d of index.docs) {
    if (options.exclude?.has(d.c.key) || (d.c.kind === "skill" && options.worn?.has(d.c.name))) continue;
    const words: string[] = [];
    let score = 0;
    let rareName = false;
    for (const t of query) {
      const w = d.name.has(t) ? 3 : d.tags.has(t) ? 2 : d.desc.has(t) ? 1 : 0;
      if (w === 0) continue;
      words.push(t);
      score += index.idf(t) * w;
      if (d.name.has(t) && index.nameDf(t) <= RARE_NAME_WORD) rareName = true;
    }
    if (words.length >= 2 || (words.length === 1 && rareName && query.length <= SHORT_REQUEST)) hits.push({ d, words, score });
  }
  hits.sort((a, b) => b.words.length - a.words.length || b.score - a.score);
  // An expert that wears a matched skill pack stands for it.
  const worn = new Set(hits.filter((h) => h.d.c.kind === "expert").flatMap((h) => h.d.c.wears));
  hits = hits.filter((h) => !(h.d.c.kind === "skill" && worn.has(h.d.c.name)));
  const [best, next] = hits;
  if (!best) return null;
  if (next && next.words.length === best.words.length && best.score < CLOSE * next.score) return null;
  return { candidate: best.d.c, matched: best.words };
}

// ---- the organization's decision model places a request ---------------------
// The same candidates, asked as one choice question of the organization's
// decision model (docs/prd/2026-10-17-decision-models.md): it reads intent and
// any language, and its probabilities are calibrated, so "clear enough" is a
// threshold, not a tuned score. The keyword matcher above is what runs when the
// model is off, unavailable or throttled. What is asked, how often and what an
// answer must be are all here; lib/decisions.ts owns the call.

/** MAX_ASKED_CANDIDATES bounds the question: more eligible candidates than this
 *  are cut to the ones whose words are closest to the request. */
export const MAX_ASKED_CANDIDATES = 120;
const ASK_HEAD_CHARS = 800;
const ASK_TAIL_CHARS = 400;
const ASK_DESCRIPTION_CHARS = 160;
/** ASK_MAX_PER_DRAFT: a draft is placed at most this many times, however long it grows. */
export const ASK_MAX_PER_DRAFT = 4;
/** After a first ask, another needs the draft this many times as long... */
const ASK_MIN_GROWTH = 1.8;
/** ...and at least this many characters more... */
const ASK_MIN_ADDED = 40;
/** ...and this long since the last. */
export const ASK_MIN_GAP_MS = 5_000;
/** DECISION_BACKOFF_MS: how long a refusal, a rate limit or a failure keeps the
 *  decision model out of it (the keyword matcher runs meanwhile). */
export const DECISION_BACKOFF_MS = 60_000;
/** SURE: the least probability a choice needs, and LEAD how many times the
 *  runner-up's (or "none") it must be. */
const SURE = 0.6;
const LEAD = 2;
const NONE = "none";

export interface AskState {
  readonly asks: number;
  readonly lastLen: number;
  readonly lastAt: number;
}
/** NO_ASKS: a new draft, not asked about yet. */
export const NO_ASKS: AskState = { asks: 0, lastLen: 0, lastAt: 0 };

/** shouldAsk: whether the draft, as it stands after a pause, is worth another
 *  question. A draft is asked about when it first says enough; again only once it
 *  has grown a lot and a while has passed; never more than ASK_MAX_PER_DRAFT
 *  times, so a long text costs a handful of calls, not one per keystroke. */
export function shouldAsk(state: AskState, draft: string, now: number): boolean {
  const len = draft.trim().length;
  if (len < MIN_DRAFT_CHARS || state.asks >= ASK_MAX_PER_DRAFT) return false;
  if (state.asks === 0) return true;
  return now - state.lastAt >= ASK_MIN_GAP_MS && len >= state.lastLen * ASK_MIN_GROWTH && len - state.lastLen >= ASK_MIN_ADDED;
}

/** asked is the state after a question about the draft. */
export function asked(state: AskState, draft: string, now: number): AskState {
  return { asks: state.asks + 1, lastLen: draft.trim().length, lastAt: now };
}

/** askedText is the part of a draft the model reads: all of a short one, the start
 *  (what is wanted) and the end (where it has got to) of a long one. */
export function askedText(draft: string): string {
  const t = draft.trim();
  if (t.length <= ASK_HEAD_CHARS + ASK_TAIL_CHARS) return t;
  return `${t.slice(0, ASK_HEAD_CHARS)}\n[…]\n${t.slice(-ASK_TAIL_CHARS)}`;
}

/** eligibleForAsk are the candidates worth asking about: not excluded (picked, on,
 *  dismissed), no skill pack an expert in effect wears; at most MAX_ASKED_CANDIDATES,
 *  the closest in words first when there are more. */
export function eligibleForAsk(draft: string, candidates: readonly UseCandidate[], options: SuggestOptions = {}): UseCandidate[] {
  const open = candidates.filter((c) => !options.exclude?.has(c.key) && !(c.kind === "skill" && options.worn?.has(c.name)));
  if (open.length <= MAX_ASKED_CANDIDATES) return open;
  const index = indexOf(candidates);
  const query = [...new Set(tokens(draft))];
  const closeness = new Map<string, number>();
  for (const d of index.docs) {
    let score = 0;
    for (const t of query) score += index.idf(t) * (d.name.has(t) ? 3 : d.tags.has(t) ? 2 : d.desc.has(t) ? 1 : 0);
    closeness.set(d.c.key, score);
  }
  return [...open].sort((a, b) => (closeness.get(b.key) ?? 0) - (closeness.get(a.key) ?? 0) || a.key.localeCompare(b.key)).slice(0, MAX_ASKED_CANDIDATES);
}

const QUESTION =
  "A person is writing a request to an AI agent in a team workspace. Choose the one expert, skill pack or tool from the list that would help most with this request. " +
  'Choose "none" when nothing clearly fits, or the person is only chatting or asking something no listed item is for.';

/** decisionRequest is the one choice question over the eligible candidates and
 *  "none". The draft is the input, never part of the instructions. */
export function decisionRequest(draft: string, eligible: readonly UseCandidate[]): DecisionRequest {
  const choices = eligible.map((c) => ({ value: c.key, description: `${c.label}. ${c.description.replace(/\s+/g, " ").trim().slice(0, ASK_DESCRIPTION_CHARS)}` }));
  choices.push({ value: NONE, description: "None of these fits." });
  return { input: askedText(draft), questions: [{ type: "choice", name: "fit", instructions: QUESTION, choices }] };
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
  return { candidate: best.candidate, matched: [], probability: best.p };
}

// ---- the member's choice of how suggestions are placed -----------------------

/** aiSuggestionsOn: whether suggestions are placed by the organization's AI (what
 *  is typed is sent to it while typing). On unless the member turned it off. */
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

/** SuggestionAction: what the row's button does, in the member's words. */
export function suggestionAction(c: UseCandidate): string {
  return c.kind === "expert" ? "Use" : c.kind === "skill" ? "Turn on" : "Add";
}

/** suggestionText: the row's sentence. */
export function suggestionText(c: UseCandidate): string {
  return `${c.label} fits this request`;
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
