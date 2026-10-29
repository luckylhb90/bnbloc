// The channels' assists (docs/prd/2026-10-28-channels-and-threads.md §4),
// their pure side: the task names (resolved by name; their output contracts
// are wire.AssistContracts, the prompts are content), the text each is asked
// with, and what the web takes from an answer. The requests are
// lib/assist.ts. An answer only proposes: the member edits a draft before
// saving it, and a suggested channel is one the web listed (any other id is
// dropped, never guessed). With no model, every control works by hand.
import { fieldText } from "./assistDrafts.ts";
import {
  AssistTaskDraftChannelPurpose,
  AssistTaskDraftRoomRules,
  AssistTaskDraftThreadTitle,
  AssistTaskFindChannel,
  AssistTaskSummarizeThread,
  MaxAssistText,
  MaxChannelPurpose,
  MaxRoomRules,
  type AssistJSONReply,
  type RoomChannel,
} from "./wire.gen.ts";

export const DRAFT_CHANNEL_PURPOSE = AssistTaskDraftChannelPurpose;
export const DRAFT_ROOM_RULES = AssistTaskDraftRoomRules;
export const FIND_CHANNEL = AssistTaskFindChannel;
// Threads (§2.2): a title drafted from the root and first replies, and the
// summary a closing thread posts to its root (lib/threads builds the text
// and reads the answer).
export const DRAFT_THREAD_TITLE = AssistTaskDraftThreadTitle;
export const SUMMARIZE_THREAD = AssistTaskSummarizeThread;

/** What the web says where an assist's button would be when no model is
 *  set up for the member (or the task is missing): the controls work by
 *  hand. */
export const AI_UNAVAILABLE = "AI suggestions are unavailable here; fill it in by hand.";

const cut = (s: string, n: number) => (s.length > n ? `${s.slice(0, n)}…` : s);

/** channelListing is the channels as an assist reads them: one line each,
 *  the id first (the answer names channels by it), the name, the purpose
 *  and the rules, cut so the whole stays within MaxAssistText. */
export function channelListing(channels: readonly Pick<RoomChannel, "channelId" | "name" | "purpose" | "rules">[], budget: number = MaxAssistText - 2000): string {
  const lines: string[] = [];
  let used = 0;
  for (const c of channels) {
    const line = `- ${c.channelId} #${c.name}${c.purpose ? `: ${c.purpose}` : ""}${c.rules ? ` (rules: ${cut(c.rules.replace(/\s+/g, " "), 200)})` : ""}`;
    if (used + line.length + 1 > budget) {
      lines.push(`(${channels.length - lines.length} more channels left out)`);
      break;
    }
    lines.push(line);
    used += line.length + 1;
  }
  return lines.join("\n");
}

/** purposeAsk is draft-channel-purpose's text: the channel's name (the
 *  channel's recent messages come as the room context, by its channelId). */
export function purposeAsk(name: string, current: string): string {
  return `Channel: #${name}\nCurrent purpose: ${current.trim() || "(none)"}`;
}

/** purposeOf is a draft purpose, within the purpose's bound ("" for none). */
export function purposeOf(fields: AssistJSONReply["fields"]): string {
  return cut(fieldText(fields, "purpose").replace(/\s+/g, " "), MaxChannelPurpose - 1);
}

/** rulesAsk is draft-room-rules' text: the room's name and description and
 *  its channels, with the current rules and welcome to improve on. */
export function rulesAsk(room: { readonly name: string; readonly description: string; readonly rules: string; readonly welcome: string }, channels: readonly Pick<RoomChannel, "channelId" | "name" | "purpose" | "rules">[]): string {
  const head = [
    `Room: ${room.name}`,
    `Description: ${room.description.trim() || "(none)"}`,
    `Current rules: ${cut(room.rules.trim(), 3000) || "(none)"}`,
    `Current welcome: ${cut(room.welcome.trim(), 1500) || "(none)"}`,
    "Channels:",
  ].join("\n");
  return `${head}\n${channelListing(channels, MaxAssistText - head.length - 100)}`;
}

/** rulesDraftOf is a draft of the room's rules and welcome ("" for a field
 *  the answer left out), within the rules' bound. */
export function rulesDraftOf(fields: AssistJSONReply["fields"]): { readonly rules: string; readonly welcome: string } {
  return { rules: cut(fieldText(fields, "rules"), MaxRoomRules - 1), welcome: fieldText(fields, "welcome") };
}

/** findAsk is find-channel's text: the question and the channels. */
export function findAsk(question: string, channels: readonly Pick<RoomChannel, "channelId" | "name" | "purpose" | "rules">[]): string {
  const head = `Question: ${cut(question.trim(), 1000)}\nChannels:`;
  return `${head}\n${channelListing(channels, MaxAssistText - head.length - 100)}`;
}

/** A channel the answer suggests, with why. */
export interface ChannelSuggestion {
  readonly channelId: string;
  readonly why: string;
}

/** findAnswerOf reads find-channel's answer: its sentence and the channels
 *  it suggests, only those the web listed (by id, each once, at most three:
 *  an id the web did not list is dropped, never matched by name). */
export function findAnswerOf(fields: AssistJSONReply["fields"], listed: readonly Pick<RoomChannel, "channelId">[]): { readonly answer: string; readonly channels: readonly ChannelSuggestion[] } {
  const raw = Array.isArray(fields.channels) ? fields.channels : [];
  const out: ChannelSuggestion[] = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const id = typeof (item as Record<string, unknown>).channelId === "string" ? ((item as Record<string, unknown>).channelId as string).trim() : "";
    const why = typeof (item as Record<string, unknown>).why === "string" ? ((item as Record<string, unknown>).why as string).trim() : "";
    if (!id || out.some((s) => s.channelId === id) || !listed.some((c) => c.channelId === id)) continue;
    out.push({ channelId: id, why });
    if (out.length === 3) break;
  }
  return { answer: fieldText(fields, "answer"), channels: out };
}
