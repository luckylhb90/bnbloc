import type { ChatAttachment, TurnMetrics } from "@/lib/api";
import type { AgentOptions } from "@/views/composer/types";
import type { DispatchAttribution } from "@/lib/dispatch";
import type { ChatExpert, ExpertMarker } from "@/lib/contentComponents";

// The routing of one chat send, exactly as it went (or will go) on the wire.
export interface ChatRecipient {
  readonly ownerId: string;
  readonly provider: string;
  readonly core?: string;
  readonly agentOptions?: AgentOptions;
}

// OutgoingMessage is the full request an own message was sent with. A retry
// re-sends exactly this — same text, recipient, options, channel, attachments
// and (via the row) the same clientMsgId — never a recipient re-derived from
// whatever the composer happens to have selected now.
export interface OutgoingMessage {
  readonly text: string;
  readonly recipient: ChatRecipient | null;
  readonly channelId: string | null;
  readonly attachmentIds: readonly string[];
  // True when the recipient is another member's agent: every (re)send must
  // pass the owner's borrow authorization first.
  readonly borrow: boolean;
  // The expert the agent is asked to wear (an agent recipient only).
  readonly expert?: ChatExpert;
}

export interface FeedItem {
  id: number;
  seq?: number;
  author: string;
  agentLabel?: string;
  text: string;
  attachments?: readonly ChatAttachment[];
  metrics?: TurnMetrics;
  thinking?: string;
  // Set on an own message that borrowed another member's agent (their id).
  borrowedFrom?: string;
  self: boolean;
  createdAtMs?: number;
  // Delivery state for the user's own optimistic messages: "pending" until
  // the room stream echoes it (the POST ack only records its sequence),
  // "sent" once echoed, "failed" when the send errored (retry re-sends
  // `outgoing`).
  status?: "pending" | "sent" | "failed";
  // The request an own optimistic message was sent with (retry source).
  outgoing?: OutgoingMessage;
  // Stable client-generated id for send idempotency (retries reuse it).
  clientMsgId?: string;
  // Animate the text in (agent replies arriving live); the Message component
  // owns the animation, so the feed state does not change per frame.
  animate?: boolean;
  // Timestamp (ms) when the message was last edited by its author; drives the
  // "(edited)" marker. Undefined = never edited.
  editedAtMs?: number;
  // Source room name of a row written by a handoff import (history, not a
  // live message): rendered with an "imported" marker, never editable.
  importedFrom?: string;
  // Set when a scheduled prompt posted this message (its schedule id); the
  // feed renders a clock badge.
  scheduledId?: string;
  // Set when an agent dispatched this message on its author's behalf (room
  // tools): the feed renders "maya → via jack/codex → bob/claude".
  dispatch?: DispatchAttribution;
  // The expert the message asked its agent to wear (payload.expert).
  expert?: ExpertMarker;
  // A member's message addressed to people only (no agent recipient): no
  // agent reply is coming. Agents still read it as room context.
  peopleOnly?: boolean;
}

// Case-insensitive self match. The agent (author "agent") and other members
// never equal the subject, so they render on the opposite side. Guards against
// display-name vs userId casing (e.g. "Maya" vs "maya") that flipped alignment.
export function isSelf(author: string | undefined, subject: string | null): boolean {
  if (!author || !subject) return false;
  if (author === "agent") return false;
  return author.toLowerCase() === subject.toLowerCase();
}

// One monotonic client id source shared by every feed producer (live dispatch,
// history load, optimistic send) so ids never collide across those sources.
let feedSeq = 1;
export function nextFeedId(): number {
  return feedSeq++;
}


// The feed's measure: it spans the full chat-column width (no centred reading
// cap) — the wide-window side gutters are intentionally gone. The composer is
// full-bleed (RoomComposer) and needs no measure.
export const READING_MEASURE = "w-full";
