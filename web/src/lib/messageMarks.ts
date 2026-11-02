// A room's message marks (channels PRD §2.3): reactions, pins and the
// member's saved messages, as the room's marks listing and its
// message_marked events give them. Pure, so node tests can import it.
import type { MessageMark, MessageMarkedPayload } from "./wire.gen.ts";

/** The emoji the reaction picker offers (any one emoji is accepted). */
export const QUICK_REACTIONS = ["👍", "❤️", "😄", "🎉", "👀", "🙏"] as const;

export interface ReactionCount {
  readonly value: string;
  readonly count: number;
  readonly mine: boolean;
}

function same(a: MessageMark, b: MessageMark): boolean {
  return a.seq === b.seq && a.kind === b.kind && (a.userId ?? "") === (b.userId ?? "") && (a.value ?? "") === (b.value ?? "");
}

/** applyMark folds a put or a removal into the marks; applying the same
 *  change twice (the reply and the event) changes nothing. */
export function applyMark(marks: readonly MessageMark[], p: MessageMarkedPayload): MessageMark[] {
  const rest = marks.filter((m) => !same(m, p.mark));
  return p.removed ? rest : [...rest, p.mark];
}

/** reactionsOf is a message's reactions by emoji, first used first. */
export function reactionsOf(marks: readonly MessageMark[], seq: number, me: string | null): ReactionCount[] {
  const by = new Map<string, { count: number; mine: boolean }>();
  for (const m of marks) {
    if (m.seq !== seq || m.kind !== "reaction" || !m.value) continue;
    const cur = by.get(m.value) ?? { count: 0, mine: false };
    by.set(m.value, { count: cur.count + 1, mine: cur.mine || (me !== null && m.userId === me) });
  }
  return [...by].map(([value, c]) => ({ value, ...c }));
}

export function isPinned(marks: readonly MessageMark[], seq: number): boolean {
  return marks.some((m) => m.seq === seq && m.kind === "pin");
}

export function isSaved(marks: readonly MessageMark[], seq: number, me: string | null): boolean {
  return me !== null && marks.some((m) => m.seq === seq && m.kind === "saved" && m.userId === me);
}

/** pinnedIn is the pinned messages of one channel, newest first. */
export function pinnedIn(marks: readonly MessageMark[], channelId: string | null): MessageMark[] {
  return marks.filter((m) => m.kind === "pin" && (m.channelId ?? "") === (channelId ?? "")).sort((a, b) => b.seq - a.seq);
}
