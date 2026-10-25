// Suggest what to use (docs/prd/2026-10-25-suggest-what-to-use.md): from what a
// member is writing, the one expert, skill pack or MCP tool of their Library that
// fits it, or nothing. Pure and local: the draft never leaves the browser, the
// ranking is a keyword score over names, tags and descriptions the client already
// holds. The one owner of the tokenizing, the scoring and the "clear enough to
// show" rules; the hook (views/composer/useUseSuggestion.ts) loads the Library and
// applies the click, the row (views/composer/UseSuggestionRow.tsx) draws.
import type { ContentComponent, MemberComponent } from "./wire.gen.ts";

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
  /** The words of the request that matched, for the row's tooltip. */
  readonly matched: readonly string[];
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

function stem(w: string): string {
  const same = SAME[w];
  if (same) return same;
  if (w.length > 5 && w.endsWith("ing")) return w.slice(0, -3);
  if (w.length > 4 && w.endsWith("ies")) return w.slice(0, -3) + "y";
  if (w.length > 4 && w.endsWith("ed")) return w.slice(0, -2);
  if (w.length > 3 && w.endsWith("s") && !w.endsWith("ss")) return w.slice(0, -1);
  return w;
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
