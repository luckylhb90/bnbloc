// Catch me up and translate (channels PRD §4), their pure side: the text
// each assist is asked with and what the web shows of an answer. The
// prompts are content (catch-up, translate); the contracts are
// wire.AssistContracts. Both answer only the member who asked.
import { fieldList, fieldText } from "./assistDrafts.ts";
import { AssistTaskCatchUp, AssistTaskTranslate, MaxAssistText, type AssistJSONReply } from "./wire.gen.ts";

export const CATCH_UP = AssistTaskCatchUp;
export const TRANSLATE = AssistTaskTranslate;

export interface CatchUpLine {
  readonly seq: number;
  readonly author: string;
  readonly text: string;
}

/** catchUpLines are the messages from fromSeq on (the member's first
 *  unread), oldest first, the newest kept when they do not all fit. */
export function catchUpLines(feed: readonly { readonly seq?: number; readonly author: string; readonly text: string }[], fromSeq: number | null): CatchUpLine[] {
  if (fromSeq === null) return [];
  return feed.filter((m) => (m.seq ?? 0) >= fromSeq && m.text.trim() !== "").map((m) => ({ seq: m.seq ?? 0, author: m.author, text: m.text }));
}

/** catchUpAsk is catch-up's text: who asks, the channel, then the unread
 *  messages, within MaxAssistText (the oldest dropped first). */
export function catchUpAsk(me: string, channel: string, lines: readonly CatchUpLine[], budget: number = MaxAssistText - 500): string {
  const head = `Member: ${me}\nChannel: #${channel}\nMessages since they last read it, oldest first:`;
  const out: string[] = [];
  let used = head.length;
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = `[${lines[i].seq}] ${lines[i].author}: ${lines[i].text.replace(/\s+/g, " ").slice(0, 1500)}`;
    if (used + l.length + 1 > budget) {
      out.unshift(`(${i + 1} earlier messages left out)`);
      break;
    }
    out.unshift(l);
    used += l.length + 1;
  }
  return [head, ...out].join("\n");
}

export interface CatchUp {
  readonly summary: string;
  readonly decided: readonly string[];
  readonly open: readonly string[];
  readonly forYou: readonly string[];
}

export function catchUpOf(fields: AssistJSONReply["fields"]): CatchUp {
  return { summary: fieldText(fields, "summary"), decided: fieldList(fields, "decided"), open: fieldList(fields, "open"), forYou: fieldList(fields, "forYou") };
}

/** translateAsk is translate's text: the language to write in (the
 *  member's browser language; decision: pending owner), then the message. */
export function translateAsk(language: string, text: string): string {
  return `Language: ${language || "en"}\nMessage:\n${text.slice(0, MaxAssistText - 200)}`;
}
