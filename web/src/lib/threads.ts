// Threads (docs/prd/2026-10-28-channels-and-threads.md §2.2): a thread is a
// channel opened from one message (its root). The pure rules the room view
// draws them by: which root a thread hangs under, what its root shows, the
// order the rail's "Threads" entry lists followed threads in, and a reply's
// quote. Unit-tested (threads.test.ts).
import type { ReplyRef, RoomChannel, ThreadInfo, ThreadUpdatedPayload } from "./wire.gen.ts";

/** One thread as its root's line shows it. */
export interface ThreadSummary {
  readonly threadId: string;
  readonly title: string;
  readonly archived: boolean;
  readonly info: ThreadInfo;
}

/** A thread's summary from its channel (null for a channel that is no thread). */
export function summaryOf(c: RoomChannel): ThreadSummary | null {
  if (!c.thread) return null;
  return { threadId: c.channelId, title: c.name, archived: (c.archivedAtMs ?? 0) > 0, info: c.thread };
}

/** A thread's summary from a thread_updated event's payload. */
export function summaryFromUpdate(p: ThreadUpdatedPayload): ThreadSummary {
  return { threadId: p.threadId, title: p.title, archived: !!p.archived, info: p.thread };
}

/** Threads keyed by their root's sequence, for one parent channel. A newer
 *  update of a thread replaces its summary. */
export function threadsByRoot(parentId: string, summaries: readonly ThreadSummary[]): Map<number, ThreadSummary> {
  const out = new Map<number, ThreadSummary>();
  for (const s of summaries) {
    if (s.info.parentId === parentId) out.set(s.info.rootSeq, s);
  }
  return out;
}

/** mergeSummary puts one thread's newest summary into a list (by id). */
export function mergeSummary(list: readonly ThreadSummary[], s: ThreadSummary): ThreadSummary[] {
  const at = list.findIndex((x) => x.threadId === s.threadId);
  if (at < 0) return [...list, s];
  const next = list.slice();
  next[at] = s;
  return next;
}

/** The line under a root: "3 replies · alice, bob" (an archived thread says so). */
export function rootLine(s: ThreadSummary, nameOf: (user: string) => string = (u) => u): string {
  const n = s.info.replies;
  const replies = n === 0 ? "No replies yet" : n === 1 ? "1 reply" : `${n} replies`;
  const people = s.info.participants.slice(0, 3).map(nameOf).join(", ");
  const more = s.info.participants.length > 3 ? ` +${s.info.participants.length - 3}` : "";
  return [replies, people ? people + more : "", s.archived ? "closed" : ""].filter(Boolean).join(" · ");
}

/** The rail's followed threads: unread first, then newest activity. */
export function followedOrder(threads: readonly RoomChannel[], unread: Readonly<Record<string, number>>): RoomChannel[] {
  const last = (c: RoomChannel) => c.thread?.lastReplyAtMs ?? 0;
  return threads
    .filter((c) => c.joined && c.thread)
    .slice()
    .sort((a, b) => Number((unread[b.channelId] ?? 0) > 0) - Number((unread[a.channelId] ?? 0) > 0) || last(b) - last(a) || a.channelId.localeCompare(b.channelId));
}

/** replyRefOf reads a chat message's payload.replyTo (undefined when absent
 *  or malformed). */
export function replyRefOf(v: unknown): ReplyRef | undefined {
  if (!v || typeof v !== "object") return undefined;
  const r = v as Record<string, unknown>;
  if (typeof r.seq !== "number" || r.seq < 1) return undefined;
  return {
    seq: r.seq,
    ...(typeof r.author === "string" && r.author ? { author: r.author } : {}),
    ...(typeof r.excerpt === "string" && r.excerpt ? { excerpt: r.excerpt } : {}),
  };
}

/** A thread's text for its assists (its title, root and replies), bounded. */
export function threadAssistText(title: string, lines: readonly { readonly author: string; readonly text: string }[], max = 12000): string {
  let out = `Title: ${title}\n`;
  for (const l of lines) {
    const line = `${l.author}: ${l.text.replace(/\s+/g, " ").trim()}\n`;
    if (out.length + line.length > max) break;
    out += line;
  }
  return out;
}

/** The reply posted to a thread's root when it closes, from the
 *  summarize-thread assist's fields (decided, open, summary). */
export function closingSummary(fields: { readonly decided?: unknown; readonly open?: unknown; readonly summary?: unknown }): string {
  const list = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string" && x.trim() !== "") : []);
  const decided = list(fields.decided);
  const open = list(fields.open);
  const summary = typeof fields.summary === "string" ? fields.summary.trim() : "";
  const parts: string[] = [];
  if (summary) parts.push(summary);
  if (decided.length) parts.push("Decided:\n" + decided.map((d) => `- ${d}`).join("\n"));
  if (open.length) parts.push("Open:\n" + open.map((d) => `- ${d}`).join("\n"));
  return parts.join("\n\n");
}
