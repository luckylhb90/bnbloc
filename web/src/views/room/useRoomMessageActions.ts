import { useCallback, useMemo, type Dispatch, type SetStateAction } from "react";
import { api, ApiError, type ChatAttachment, type Room } from "@/lib/api";
import { isPlatformOffline, PLATFORM_OFFLINE_MESSAGE } from "@/lib/platformAgents";
import { onboardingEvents } from "@/lib/onboardingEvents";
import { borrowTarget } from "@/lib/roomAgents";
import { useToast } from "@/ui/Toast";
import type { RoomAgent } from "@/views/composer/types";
import type { FileRef } from "@/lib/wire.gen";
import { ackSent, markStatus, outgoingMessage } from "@/views/room/feedModel";
import { type ChatRecipient, type FeedItem, type OutgoingMessage, nextFeedId } from "@/views/room/types";
import { displayNameOf } from "@/lib/useDisplayName";
import type { ChatExpert, ExpertMarker } from "@/lib/contentComponents";
import { errorText, statusText } from "@/lib/errors";

export interface SendOptions {
  readonly peopleOnly: boolean;
  readonly recipient: RoomAgent | null;
  readonly attachmentIds?: readonly string[];
  readonly attachments?: readonly ChatAttachment[];
  // The expert the agent recipient wears, and its marker for the
  // optimistic row.
  readonly expert?: ChatExpert;
  readonly expertMarker?: ExpertMarker;
  // The room file the message is about (the workbench's Ask AI panel).
  readonly fileRef?: FileRef;
}

export interface RoomMessageActions {
  // Resolves true when the message is owned by the feed (accepted, or kept as
  // a failed row the user can retry) — the composer may clear its draft. False
  // means it was rejected outright (borrow not authorized, not a member, too
  // long…): nothing was kept, so the composer keeps the draft.
  send: (text: string, opts: SendOptions) => Promise<boolean>;
  retry: (item: FeedItem) => Promise<void>;
  deleteOwnMessage: (item: FeedItem) => Promise<void>;
  editOwnMessage: (item: FeedItem, text: string) => Promise<void>;
}

type Delivery = "accepted" | "rejected" | "failed";

// useRoomMessageActions owns the optimistic message lifecycle: send (with the
// fail-closed borrow check), retry, soft-delete, and edit. Each applies an
// optimistic feed change and reconciles or rolls back on the server's answer;
// the realtime echo/broadcast reconciles for every other member.
export function useRoomMessageActions({
  roomId,
  channelId,
  subject,
  setFeed,
  setRoom,
  onStaleChannel,
  onQuotaExhausted,
  onPlatformOffline,
}: {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly subject: string | null;
  readonly setFeed: Dispatch<SetStateAction<FeedItem[]>>;
  readonly setRoom: Dispatch<SetStateAction<Room | null>>;
  readonly onStaleChannel: () => void;
  // A borrow refused because the owner's run limit is used up (F3): the room
  // offers "Request more" for that agent.
  readonly onQuotaExhausted?: (recipient: ChatRecipient, runQuota?: number) => void;
  // A platform agent's host went offline before the send (409
  // platform_agent_offline): the room re-reads its hosts' state.
  readonly onPlatformOffline?: () => void;
}): RoomMessageActions {
  const toast = useToast();

  // ensureBorrow verifies the agent's owner has authorized room-wide borrowing
  // of this (core, provider) before sending — the same core-qualified identity
  // the owner authorized and the bridge enforces. Borrowers cannot self-grant.
  // Returns false (with a reason) when there is no live authorization, so the
  // message is not sent into a fail-closed drive that would only post a denial.
  const ensureBorrow = useCallback(
    async (recipient: ChatRecipient): Promise<boolean> => {
      try {
        const res = await api.borrowAuthorization(roomId, borrowTarget(recipient), recipient.ownerId);
        if (res.authorized && res.quotaExhausted) {
          toast.info(`You've used all the runs ${displayNameOf(recipient.ownerId)} authorized for their ${recipient.provider} here`);
          onQuotaExhausted?.(recipient, res.runQuota);
          return false;
        }
        if (res.authorized) return true;
        toast.error(`${displayNameOf(recipient.ownerId)} has not authorized borrowing their ${recipient.provider} here`);
        return false;
      } catch (e) {
        toast.error(errorText(e, "Borrow check failed"));
        return false;
      }
    },
    [roomId, toast, onQuotaExhausted],
  );

  // deliver posts one outgoing message for the row `localId` and records the
  // server's sequence on success. The row stays pending until the stream echo
  // confirms it (feedModel.applyIncoming / ackSent).
  const deliver = useCallback(
    async (localId: number, clientMsgId: string, out: OutgoingMessage): Promise<Delivery> => {
      try {
        const { sequence } = await api.sendChat(roomId, out, clientMsgId);
        setFeed((f) => ackSent(f, localId, sequence));
        onboardingEvents.emit({ kind: "message_sent", roomId, source: "action", seq: sequence, toAgent: !!out.recipient });
        return "accepted";
      } catch (e) {
        const status = e instanceof ApiError ? e.status : 0;
        if (status === 403) {
          toast.info("Join this room to chat");
          api.getRoom(roomId).then(setRoom).catch(() => {});
          return "rejected";
        }
        if (status === 404) {
          toast.error("This channel no longer exists — switched to the room's default channel");
          onStaleChannel();
          return "rejected";
        }
        if (isPlatformOffline(status, errorText(e, ""))) {
          toast.error(PLATFORM_OFFLINE_MESSAGE);
          onPlatformOffline?.();
          return "rejected";
        }
        if (status === 413) {
          toast.error("Message is too long");
          return "rejected";
        }
        if (status === 400) {
          toast.error(errorText(e, "Message rejected"));
          return "rejected";
        }
        // Explain the rate limit so the user does not just retry into it;
        // other failures keep the retry affordance on the row.
        if (status === 429) toast.error(statusText(429));
        return "failed";
      }
    },
    [roomId, setFeed, setRoom, onStaleChannel, onPlatformOffline, toast],
  );

  const send = useCallback(
    async (text: string, opts: SendOptions): Promise<boolean> => {
      const out = outgoingMessage(text, opts.peopleOnly ? null : opts.recipient, channelId, opts.attachmentIds, opts.expert, opts.fileRef);
      if (out.borrow && out.recipient && !(await ensureBorrow(out.recipient))) return false;
      const localId = nextFeedId();
      const clientMsgId = crypto.randomUUID();
      setFeed((f) => [
        ...f,
        {
          id: localId,
          author: subject || "me",
          text,
          self: true,
          createdAtMs: Date.now(),
          status: "pending",
          outgoing: out,
          clientMsgId,
          attachments: opts.attachments ? [...opts.attachments] : undefined,
          borrowedFrom: out.borrow ? out.recipient?.ownerId : undefined,
          peopleOnly: out.recipient ? undefined : true,
          expert: out.expert ? opts.expertMarker : undefined,
          fileRef: out.fileRef,
        },
      ]);
      const result = await deliver(localId, clientMsgId, out);
      if (result === "rejected") {
        setFeed((f) => f.filter((m) => m.id !== localId));
        return false;
      }
      if (result === "failed") setFeed((f) => markStatus(f, localId, "failed"));
      return true;
    },
    [channelId, deliver, ensureBorrow, setFeed, subject],
  );

  // retry re-sends a failed message EXACTLY as it was first sent — same text,
  // recipient, model options, channel, attachments and clientMsgId (so the
  // server dedupes a send that did land) — re-checking the borrow grant,
  // which may have been revoked or expired meanwhile.
  const retry = useCallback(
    async (item: FeedItem) => {
      const out = item.outgoing;
      if (!out || !item.clientMsgId) return;
      if (out.borrow && out.recipient && !(await ensureBorrow(out.recipient))) return;
      setFeed((f) => markStatus(f, item.id, "pending"));
      const result = await deliver(item.id, item.clientMsgId, out);
      if (result !== "accepted") setFeed((f) => markStatus(f, item.id, "failed"));
    },
    [deliver, ensureBorrow, setFeed],
  );

  // deleteOwnMessage deletes a message the user authored. A failed send that
  // never reached the server is simply discarded locally. A stored message is
  // removed optimistically; the server broadcasts message_deleted to everyone
  // and a rejection restores the row in place.
  const deleteOwnMessage = useCallback(
    async (item: FeedItem) => {
      if (item.seq == null) {
        if (item.status === "failed") setFeed((f) => f.filter((m) => m.id !== item.id));
        return;
      }
      const seq = item.seq;
      let index = -1;
      setFeed((f) => {
        index = f.findIndex((m) => m.id === item.id);
        return index === -1 ? f : f.filter((m) => m.id !== item.id);
      });
      try {
        await api.deleteMessage(roomId, seq);
      } catch (e) {
        toast.error(errorText(e, "Delete failed"));
        setFeed((f) => {
          if (f.some((m) => m.id === item.id)) return f;
          const next = [...f];
          next.splice(index === -1 ? next.length : Math.min(index, next.length), 0, item);
          return next;
        });
      }
    },
    [roomId, setFeed, toast],
  );

  // editOwnMessage updates the text of a message the user authored, applied
  // optimistically (with an "(edited)" marker) and rolled back on rejection.
  // The realtime message_edited event reconciles it for every other member.
  const editOwnMessage = useCallback(
    async (item: FeedItem, text: string) => {
      if (item.seq == null) return;
      const seq = item.seq;
      const next = text.trim();
      if (!next || next === item.text) return;
      const prevText = item.text;
      const prevEdited = item.editedAtMs;
      setFeed((f) => f.map((m) => (m.id === item.id ? { ...m, text: next, editedAtMs: Date.now() } : m)));
      try {
        await api.editMessage(roomId, seq, next);
      } catch (e) {
        toast.error(errorText(e, "Edit failed"));
        setFeed((f) => f.map((m) => (m.id === item.id ? { ...m, text: prevText, editedAtMs: prevEdited } : m)));
      }
    },
    [roomId, setFeed, toast],
  );

  return useMemo(
    () => ({ send, retry, deleteOwnMessage, editOwnMessage }),
    [send, retry, deleteOwnMessage, editOwnMessage],
  );
}
