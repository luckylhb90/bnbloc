// Pure helpers for the team features (prompt library, message tasks, global
// search) plus the two cross-view hand-offs they need: a composer draft queued
// for a room ("Use" a prompt) and a message jump queued for a room (search /
// task "jump to message"). Both hand-offs are in-memory: the target room view
// consumes them on mount, or immediately when it is already open.
import type { MessageTask, SearchResult, TaskUpdate } from "./teamModels.ts";
import type { Referenceable } from "../views/composer/types.ts";

// promptTitleFromText proposes a library title: the message's first non-empty
// line, trimmed to a readable length.
export function promptTitleFromText(text: string, max = 80): string {
  const line = text.split("\n").map((l) => l.trim()).find((l) => l !== "") ?? "";
  const plain = line.replace(/^#+\s*/, "");
  return plain.length > max ? `${plain.slice(0, max - 1).trimEnd()}…` : plain;
}

// parseTags splits a comma/whitespace separated tag field into normalized,
// de-duplicated tags (the server applies the same normalization).
export function parseTags(input: string): string[] {
  const out: string[] = [];
  for (const raw of input.split(/[,\s]+/)) {
    const t = raw.trim().toLowerCase().replace(/^#/, "");
    if (t && !out.includes(t)) out.push(t);
  }
  return out;
}

// highlightParts splits text around case-insensitive occurrences of query so
// a view can emphasise the matches.
export function highlightParts(text: string, query: string): { text: string; match: boolean }[] {
  const q = query.trim().toLowerCase();
  if (!q) return [{ text, match: false }];
  const lower = text.toLowerCase();
  const parts: { text: string; match: boolean }[] = [];
  let at = 0;
  for (let i = lower.indexOf(q); i !== -1; i = lower.indexOf(q, at)) {
    if (i > at) parts.push({ text: text.slice(at, i), match: false });
    parts.push({ text: text.slice(i, i + q.length), match: true });
    at = i + q.length;
  }
  if (at < text.length) parts.push({ text: text.slice(at), match: false });
  return parts;
}

// mergeDraft places an injected prompt into the composer: it replaces an
// empty draft and is appended (on a new paragraph) to a non-empty one, so
// "Use" never silently discards what the member was typing.
export function mergeDraft(current: string, injected: string): string {
  return current.trim() ? `${current.replace(/\s+$/, "")}\n\n${injected}` : injected;
}

// taskSeq maps a feed row's sequence to its task key: agent replies in one
// batch carry fractional sequences (base + i/1000) and share the batch's room
// sequence, which is what the server stores.
export function taskSeq(seq: number | undefined): number | null {
  return typeof seq === "number" ? Math.floor(seq) : null;
}

// applyTaskUpdate folds one task_updated event into a room's task list
// (ordered by message sequence).
export function applyTaskUpdate(tasks: readonly MessageTask[], update: TaskUpdate): MessageTask[] {
  const rest = tasks.filter((t) => t.seq !== update.seq);
  if (update.removed || !update.task) return rest;
  return [...rest, update.task].sort((a, b) => a.seq - b.seq);
}

export const SEARCH_KIND_LABELS: Record<SearchResult["kind"], string> = {
  message: "Messages",
  artifact: "Documents",
  prompt: "Prompts",
};

// groupResults orders search hits by kind (messages, documents, prompts),
// keeping the server's order within a kind. The flat order is the keyboard
// navigation order.
export function groupResults(results: readonly SearchResult[]): { kind: SearchResult["kind"]; items: SearchResult[] }[] {
  const kinds: SearchResult["kind"][] = ["message", "artifact", "prompt"];
  return kinds
    .map((kind) => ({ kind, items: results.filter((r) => r.kind === kind) }))
    .filter((g) => g.items.length > 0);
}

// ---- cross-view hand-offs

export interface MessageJump {
  readonly seq: number;
  readonly channelId?: string;
}

type Listener = (roomId: string) => void;

function handoff<T>() {
  const pending = new Map<string, T>();
  const listeners = new Set<Listener>();
  return {
    queue(roomId: string, value: T) {
      pending.set(roomId, value);
      for (const l of listeners) l(roomId);
    },
    take(roomId: string): T | undefined {
      const v = pending.get(roomId);
      pending.delete(roomId);
      return v;
    },
    peek(roomId: string): T | undefined {
      return pending.get(roomId);
    },
    subscribe(l: Listener): () => void {
      listeners.add(l);
      return () => listeners.delete(l);
    },
  };
}

// composerDrafts carries a prompt body to a room's composer.
export const composerDrafts = handoff<string>();
// composerAgentPicks carries "select this platform agent" to a room's
// composer (the welcome card's "Start a room with <agent>").
export const composerAgentPicks = handoff<{ readonly core: string; readonly provider: string }>();
// messageJumps carries a "scroll to this message" request to a room's feed.
export const messageJumps = handoff<MessageJump>();
// artifactLinks carries an "open this deliverable" request (a copied
// #/room/<id>?artifact=<aid>[&v=n] link) to a room's preview workbench.
export const artifactLinks = handoff<{ readonly artifactId: string; readonly version?: number }>();
// composerRefs carries quoted references to a room's main composer (the
// people chat's "Ask the agent about this"); queued refs accumulate until the
// composer takes them.
export const composerRefs = handoff<readonly Referenceable[]>();

export function queueComposerRef(roomId: string, ref: Referenceable): void {
  composerRefs.queue(roomId, [...(composerRefs.peek(roomId) ?? []), ref]);
}

// A popped-out people chat is its own OS window (own JS heap): a reference
// asked from there reaches the main window's composer through a storage
// event (same origin, shared localStorage). The key is written then removed
// at once — only the event carries the payload, nothing lingers.
const SHARED_REF_KEY = "bnbloc.composerRef";

interface SharedRef {
  readonly roomId: string;
  readonly ref: Referenceable;
}

export function parseSharedRef(raw: string | null): SharedRef | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as Partial<SharedRef>;
    const r = v.ref as Partial<Referenceable> | undefined;
    if (typeof v.roomId !== "string" || !r || typeof r.author !== "string" || typeof r.text !== "string") return null;
    return { roomId: v.roomId, ref: { id: typeof r.id === "number" ? r.id : 0, author: r.author, text: r.text } };
  } catch {
    return null;
  }
}

// shareComposerRef queues the reference in this window and announces it to
// every other window of the app.
export function shareComposerRef(roomId: string, ref: Referenceable): void {
  queueComposerRef(roomId, ref);
  try {
    localStorage.setItem(SHARED_REF_KEY, JSON.stringify({ roomId, ref }));
    localStorage.removeItem(SHARED_REF_KEY);
  } catch {
    // storage unavailable: only this window's composer receives it
  }
}

// listenForSharedComposerRefs queues references announced by other windows.
// Returns the unsubscribe.
export function listenForSharedComposerRefs(): () => void {
  const onStorage = (e: StorageEvent) => {
    if (e.key !== SHARED_REF_KEY) return;
    const shared = parseSharedRef(e.newValue);
    if (shared) queueComposerRef(shared.roomId, shared.ref);
  };
  window.addEventListener("storage", onStorage);
  return () => window.removeEventListener("storage", onStorage);
}

// The last room the member opened: the default target of "Use" a prompt.
const LAST_ROOM_KEY = "bnbloc.lastRoom";
export function rememberLastRoom(roomId: string): void {
  try {
    localStorage.setItem(LAST_ROOM_KEY, roomId);
  } catch {
    // storage unavailable (private mode); the picker falls back to the first room
  }
}
export function lastRoom(): string | null {
  try {
    return localStorage.getItem(LAST_ROOM_KEY);
  } catch {
    return null;
  }
}

// A query handed to the Library page (a prompt hit in global search opens the
// library filtered to it). Taken once on the page's mount.
let pendingLibraryQuery: string | null = null;
export function queueLibraryQuery(q: string): void {
  pendingLibraryQuery = q;
}
export function takeLibraryQuery(): string | null {
  const q = pendingLibraryQuery;
  pendingLibraryQuery = null;
  return q;
}

// Global search is opened from anywhere (sidebar button, Cmd/Ctrl+K) through
// one window event, so the dialog itself can load lazily.
export const OPEN_SEARCH_EVENT = "bnbloc:open-search";
export function openGlobalSearch(): void {
  window.dispatchEvent(new Event(OPEN_SEARCH_EVENT));
}
