// Pure feed transitions for the room conversation. Every way a row enters or
// changes — a live stream event, a history page, the optimistic send, the
// POST ack — goes through these functions, so reconciliation rules live in
// one place and are unit-tested (feedModel.test.ts).
import type { RoomEvent } from "../../lib/sse.ts";
import type { RoomAgent } from "../composer/types.ts";
import { type FeedItem, type OutgoingMessage, isSelf, nextFeedId } from "./types.ts";

// feedItemFromEvent converts a chat/agent event (live or from history) into a
// feed row, or null for an event with no visible content. `live` marks rows
// that arrived on the stream: agent replies among them animate in.
export function feedItemFromEvent(e: RoomEvent, subject: string | null, live: boolean): FeedItem | null {
  const text = e.text ?? "";
  if (!text && (!e.attachments || e.attachments.length === 0)) return null;
  const author = e.author || "agent";
  const self = isSelf(author, subject);
  return {
    id: nextFeedId(),
    seq: e.sequence,
    author,
    agentLabel: e.agentLabel,
    text,
    clientMsgId: e.clientMsgId,
    attachments: e.attachments,
    metrics: e.metrics,
    thinking: e.thinking,
    self,
    createdAtMs: e.createdAtMs,
    editedAtMs: e.editedAtMs,
    importedFrom: e.importedFrom,
    scheduledId: e.scheduledId,
    dispatch: e.dispatch,
    peopleOnly: e.kind === "chat_message" && e.toAgent === false ? true : undefined,
    status: self ? "sent" : undefined,
    animate: live && author === "agent" ? true : undefined,
  };
}

// confirm upgrades an own row with what the echoed event carries.
function confirm(row: FeedItem, echo: FeedItem): FeedItem {
  return {
    ...row,
    seq: echo.seq ?? row.seq,
    status: "sent",
    createdAtMs: echo.createdAtMs || row.createdAtMs,
    attachments: echo.attachments ?? row.attachments,
    metrics: echo.metrics ?? row.metrics,
    thinking: echo.thinking ?? row.thinking,
  };
}

// applyIncoming adds one live row. A row whose sequence is already present is
// a reconnect replay — or the echo of our own send whose POST ack already
// recorded the sequence, which confirms it. An own row carrying the
// clientMsgId of a pending/failed optimistic row upgrades that row in place.
// There is deliberately no text-equality matching: identity is the sequence
// (from the ack) or the clientMsgId (from the echo), never content.
export function applyIncoming(feed: readonly FeedItem[], item: FeedItem): FeedItem[] {
  if (item.seq != null) {
    const at = feed.findIndex((m) => m.seq === item.seq);
    if (at !== -1) {
      const row = feed[at];
      if (row.status !== "pending") return feed as FeedItem[];
      const next = [...feed];
      next[at] = confirm(row, item);
      return next;
    }
  }
  if (item.self && item.clientMsgId) {
    const at = feed.findIndex((m) => m.clientMsgId === item.clientMsgId && m.status !== "sent");
    if (at !== -1) {
      const next = [...feed];
      next[at] = confirm(feed[at], item);
      return next;
    }
  }
  return [...feed, item];
}

// ackSent records the sequence the server assigned to an optimistic row. If
// the echo already arrived, it either confirmed the row in place (matched by
// clientMsgId — the usual case, the server echoes it) and the row stays
// confirmed, or it was appended as its own row and the two are merged into
// the optimistic row's position and confirmed; otherwise the row stays
// pending until the echo. (Resetting a clientMsgId-confirmed row to pending
// left it "Sending…" forever whenever the echo beat the POST response.)
export function ackSent(feed: readonly FeedItem[], localId: number, seq: number): FeedItem[] {
  const row = feed.find((m) => m.id === localId);
  if (!row) return feed as FeedItem[];
  const echo = feed.find((m) => m.id !== localId && m.seq === seq);
  const next: FeedItem[] = [];
  for (const m of feed) {
    if (m === echo) continue;
    if (m.id === localId) next.push(echo ? confirm({ ...m, seq }, echo) : { ...m, seq, status: m.status === "sent" ? "sent" : "pending" });
    else next.push(m);
  }
  return next;
}

// markStatus sets the delivery status of one row.
export function markStatus(feed: readonly FeedItem[], localId: number, status: FeedItem["status"]): FeedItem[] {
  return feed.map((m) => (m.id === localId ? { ...m, status } : m));
}

// mergeHistory folds a page of persisted history into the feed: rows whose
// sequence is already present are skipped, and sequenced rows are ordered by
// sequence regardless of whether the page or the live stream landed first.
// Rows without a sequence yet (unacknowledged sends) stay after them, in
// their existing order.
export function mergeHistory(feed: readonly FeedItem[], page: readonly FeedItem[]): FeedItem[] {
  const known = new Set(feed.map((m) => m.seq).filter((s) => s != null));
  const fresh = page.filter((m) => m.seq == null || !known.has(m.seq));
  if (fresh.length === 0) return feed as FeedItem[];
  const all = [...fresh, ...feed];
  const sequenced = all.filter((m) => m.seq != null).sort((a, b) => (a.seq as number) - (b.seq as number));
  const unsequenced = all.filter((m) => m.seq == null);
  return [...sequenced, ...unsequenced];
}

// maxSequence is the newest sequence present in the feed (0 when none).
export function maxSequence(feed: readonly { readonly seq?: number }[]): number {
  let max = 0;
  for (const m of feed) if (typeof m.seq === "number" && m.seq > max) max = m.seq;
  return Math.floor(max);
}

// outgoingMessage captures the exact request for a send: the recipient (null
// for people-only), its per-send model/reasoning options, the channel and the
// uploaded attachments. Retries re-send this verbatim.
export function outgoingMessage(
  text: string,
  recipient: RoomAgent | null,
  channelId: string | null,
  attachmentIds: readonly string[] = [],
): OutgoingMessage {
  return {
    text,
    recipient: recipient
      ? {
          ownerId: recipient.ownerId,
          provider: recipient.provider,
          ...(recipient.core ? { core: recipient.core } : {}),
          ...(recipient.agentOptions ? { agentOptions: recipient.agentOptions } : {}),
        }
      : null,
    channelId,
    attachmentIds: [...attachmentIds],
    borrow: !!recipient && !recipient.self,
  };
}
