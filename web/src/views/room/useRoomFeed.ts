import { useCallback, useEffect, type Dispatch, type SetStateAction } from "react";
import { api, ApiError } from "@/lib/api";
import { normalizeRoomEvent, type RoomEvent } from "@/lib/roomEvents";
import { onboardingEventFromRoomEvent, onboardingEvents } from "@/lib/onboardingEvents";
import { useScopedState } from "@/lib/useScopedState";
import { applyIncoming, feedItemFromEvent, mergeHistory } from "@/views/room/feedModel";
import type { FeedItem } from "@/views/room/types";

const PAGE = 50;

interface HistoryState {
  // Oldest sequence loaded (the "load earlier" cursor) and whether more may
  // exist below it.
  readonly oldestSeq: number | null;
  readonly hasMore: boolean;
  readonly loading: boolean;
  // The latest page has landed for this channel visit.
  readonly loaded: boolean;
}

const EMPTY_HISTORY: HistoryState = { oldestSeq: null, hasMore: false, loading: false, loaded: false };

export interface RoomFeed {
  readonly feed: FeedItem[];
  readonly setFeed: Dispatch<SetStateAction<FeedItem[]>>;
  readonly history: HistoryState;
  // Load the page older than the current cursor ("Load earlier").
  readonly loadEarlier: () => void;
  // Apply a feed-kind live event (chat, agent reply, delete, edit). Returns
  // false for kinds the feed does not own.
  readonly applyEvent: (e: RoomEvent) => boolean;
}

// useRoomFeed owns one channel's conversation: its rows, history paging and
// live event application. All state is scoped to (room, channel): switching
// channel starts from an empty feed in the same render, and responses or
// events from the previous channel can never write into the new one.
export function useRoomFeed({
  roomId,
  channelId,
  subject,
  joinedGen,
  beforePrepend,
  onStaleChannel,
}: {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly subject: string | null;
  readonly joinedGen: number;
  // Called right before an older page is prepended, so the scroller can keep
  // the viewport anchored.
  readonly beforePrepend: () => void;
  // The channel no longer belongs to the room (404): the room resets to its
  // default channel.
  readonly onStaleChannel: () => void;
}): RoomFeed {
  const scope = channelId ?? "";
  const [feed, setFeed] = useScopedState<FeedItem[]>(scope, () => []);
  const [history, setHistory] = useScopedState<HistoryState>(scope, () => EMPTY_HISTORY);

  const loadPage = useCallback(
    async (before: number | undefined) => {
      if (!channelId) return;
      setHistory((h) => ({ ...h, loading: true }));
      try {
        const res = await api.roomHistory(roomId, before, PAGE, channelId);
        const page: FeedItem[] = [];
        let minSeq = before ?? Number.POSITIVE_INFINITY;
        for (const raw of res.messages) {
          normalizeRoomEvent(raw as RoomEvent, (e: RoomEvent) => {
            if (typeof e.sequence === "number") minSeq = Math.min(minSeq, e.sequence);
            onboardingEvents.emit(onboardingEventFromRoomEvent(e, subject, roomId, "history"));
            const item = feedItemFromEvent(e, subject, false);
            if (item) page.push(item);
          });
        }
        if (before != null) beforePrepend();
        setFeed((f) => mergeHistory(f, page));
        setHistory((h) => ({
          oldestSeq: minSeq !== Number.POSITIVE_INFINITY ? minSeq : h.oldestSeq,
          hasMore: res.messages.length >= PAGE && minSeq > 1,
          loading: false,
          loaded: true,
        }));
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) {
          onStaleChannel();
          return;
        }
        // History is best-effort; the live stream still works. A failed first
        // page still counts as "loaded" so read tracking is not wedged.
        setHistory((h) => ({ ...h, loading: false, loaded: true }));
      }
    },
    [roomId, channelId, subject, beforePrepend, onStaleChannel, setFeed, setHistory],
  );

  // The latest page on entering the channel, and again after a (re)join so the
  // newly-scoped token sees the full history.
  useEffect(() => {
    void loadPage(undefined);
  }, [loadPage, joinedGen]);

  const loadEarlier = useCallback(() => {
    if (history.loading || history.oldestSeq == null) return;
    void loadPage(history.oldestSeq);
  }, [history.loading, history.oldestSeq, loadPage]);

  const applyEvent = useCallback(
    (e: RoomEvent): boolean => {
      if (e.kind === "message_deleted") {
        if (typeof e.deletedSeq === "number") setFeed((f) => f.filter((m) => m.seq !== e.deletedSeq));
        return true;
      }
      if (e.kind === "message_edited") {
        if (typeof e.editedSeq === "number") {
          setFeed((f) =>
            f.map((m) =>
              m.seq === e.editedSeq ? { ...m, text: e.text ?? m.text, editedAtMs: e.editedAtMs ?? Date.now() } : m,
            ),
          );
        }
        return true;
      }
      if (e.kind && e.kind !== "chat_message" && e.kind !== "agent_messages") return false;
      onboardingEvents.emit(onboardingEventFromRoomEvent(e, subject, roomId, "stream"));
      const item = feedItemFromEvent(e, subject, true);
      if (item) setFeed((f) => applyIncoming(f, item));
      return true;
    },
    [roomId, subject, setFeed],
  );

  return { feed, setFeed, history, loadEarlier, applyEvent };
}
