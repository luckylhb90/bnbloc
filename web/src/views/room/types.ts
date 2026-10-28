import type { ChatAttachment, TurnMetrics } from "@/lib/api";
import type { AgentOptions } from "@/views/composer/types";
import type { DispatchAttribution } from "@/lib/dispatch";
import type { ChatExpert, ExpertMarker } from "@/lib/contentComponents";
import type { FileRef } from "@/lib/wire.gen";
import type { PublishProposal } from "@/lib/publishProposal";
import type { ShareProposal } from "@/lib/shareProposal";
import type { ComponentCard } from "@/lib/componentCard";
import { sameUser } from "../../lib/format.ts";

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
  // The room file the message is about (the workbench's Ask AI panel).
  readonly fileRef?: FileRef;
}

export interface FeedItem {
  id: number;
  // The room sequence (an integer; absent until an own send is acked).
  seq?: number;
  // The row's index within an agent_messages batch, which shares one room
  // sequence: rows order and dedupe on (seq, part) (feedModel.ts).
  part?: number;
  author: string;
  agentLabel?: string;
  // The replying agent's identity (agentIdentity) on an assistant reply; the
  // context meter finds the addressed agent's newest reply by it.
  agentKey?: string;
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
  // The service whose card decision posted this message as the member (an
  // automatic message, not typed).
  continuedBy?: string;
  // Set when an agent dispatched this message on its author's behalf (room
  // tools): the feed renders "maya → via jack/codex → bob/claude".
  dispatch?: DispatchAttribution;
  // The expert the message asked its agent to wear (payload.expert).
  expert?: ExpertMarker;
  // The room file the message is about (payload.fileRef).
  fileRef?: FileRef;
  // An agent's notice line (a team lead's progress, a member timeout):
  // rendered as a compact line, not a reply.
  notice?: string;
  // A notice's detail (payload.detail): the agent's own error output,
  // rendered fenced under the notice line, never as Markdown.
  noticeDetail?: string;
  // A publish-proposal notice's card (payload.proposal): only the person
  // the agent asked for is shown it (feedItemFromEvent drops it for others).
  proposal?: PublishProposal;
  // A share-proposal notice's card (payload.shareProposal): only the person
  // the agent asked for is shown it (feedItemFromEvent drops it for others).
  shareProposal?: ShareProposal;
  // A component-card notice's card (payload.card): only the person the
  // turn ran for is shown it (feedItemFromEvent drops it for others).
  card?: ComponentCard;
  // An agent row's job and the room sequence of that job's request
  // (payload.jobId / payload.sourceSeq): delegated work nests under the
  // request that started it (feedTurns.ts).
  jobId?: string;
  sourceSeq?: number;
  // A member's message addressed to people only (no agent recipient): no
  // agent reply is coming. Agents still read it as room context.
  peopleOnly?: boolean;
}

// Case-insensitive self match. The agent (author "agent") and other members
// never equal the subject, so they render on the opposite side. Guards against
// display-name vs userId casing (e.g. "Maya" vs "maya") that flipped alignment.
export function isSelf(author: string | undefined, subject: string | null): boolean {
  if (author === "agent") return false;
  return sameUser(author, subject);
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
