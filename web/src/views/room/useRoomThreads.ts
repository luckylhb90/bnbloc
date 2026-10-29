import { useCallback, useEffect, useMemo, useState } from "react";
import { channelsApi } from "@/lib/channelsApi";
import { errorText } from "@/lib/errors";
import type { RoomEvent } from "@/lib/roomEvents";
import { followedOrder, mergeSummary, summaryFromUpdate, summaryOf, threadsByRoot, type ThreadSummary } from "@/lib/threads";
import type { RoomChannel } from "@/lib/wire.gen";
import { useToast } from "@/ui/Toast";

export interface RoomThreads {
  // The current channel's threads by their root's sequence (the root lines).
  readonly byRoot: ReadonlyMap<number, ThreadSummary>;
  // The threads the member follows, unread first (the rail's Threads entry).
  readonly followed: readonly RoomChannel[];
  // The thread open in the side panel (null: none).
  readonly open: RoomChannel | null;
  readonly openThread: (rootSeq: number) => void;
  readonly openById: (threadId: string) => void;
  readonly close: () => void;
  readonly reload: () => void;
  // Route a stream event first: thread_updated refreshes a root's line.
  readonly applyEvent: (e: RoomEvent) => void;
}

// useRoomThreads owns a room view's threads (docs/prd/2026-10-28-channels-
// and-threads.md §2.2): the current channel's threads (one line under each
// root), the ones the member follows (unread first, lib/threads), and which
// one the side panel shows. Opening a root's thread asks realtime, which
// returns the existing thread or makes it (one per root).
export function useRoomThreads({
  roomId,
  channelId,
  joinedGen,
  enabled,
  unreadByChannel,
}: {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly joinedGen: number;
  // False for a preview (not a member): threads open only for members.
  readonly enabled: boolean;
  readonly unreadByChannel: Readonly<Record<string, number>>;
}): RoomThreads {
  const toast = useToast();
  const [summaries, setSummaries] = useState<ThreadSummary[]>([]);
  const [followedList, setFollowedList] = useState<RoomChannel[]>([]);
  const [open, setOpen] = useState<RoomChannel | null>(null);
  const [gen, setGen] = useState(0);

  useEffect(() => {
    if (!enabled || !channelId) {
      setSummaries([]);
      return;
    }
    let cancelled = false;
    channelsApi.threads(roomId, { parent: channelId }).then(
      (r) => {
        if (!cancelled) setSummaries(r.threads.map(summaryOf).filter((s): s is ThreadSummary => s !== null));
      },
      () => {
        if (!cancelled) setSummaries([]);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [roomId, channelId, joinedGen, enabled, gen]);

  useEffect(() => {
    if (!enabled) {
      setFollowedList([]);
      return;
    }
    let cancelled = false;
    channelsApi.threads(roomId, { followed: true }).then(
      (r) => {
        if (!cancelled) setFollowedList(r.threads);
      },
      () => {
        if (!cancelled) setFollowedList([]);
      },
    );
    return () => {
      cancelled = true;
    };
  }, [roomId, joinedGen, enabled, gen]);

  // The panel closes when the room or the channel it hangs under changes.
  useEffect(() => setOpen(null), [roomId]);

  const reload = useCallback(() => setGen((g) => g + 1), []);

  const openThread = useCallback(
    (rootSeq: number) => {
      if (!channelId) return;
      channelsApi.openThread(roomId, { parentId: channelId, rootSeq }).then(
        (th) => {
          setOpen(th);
          const s = summaryOf(th);
          if (s) setSummaries((cur) => mergeSummary(cur, s));
          reload();
        },
        (e: unknown) => toast.error(errorText(e, "Could not open the thread")),
      );
    },
    [roomId, channelId, toast, reload],
  );

  const openById = useCallback(
    (threadId: string) => {
      const known = followedList.find((t) => t.channelId === threadId);
      if (known) setOpen(known);
    },
    [followedList],
  );

  const close = useCallback(() => setOpen(null), []);

  const applyEvent = useCallback(
    (e: RoomEvent) => {
      if (!e.threadUpdate) return;
      const s = summaryFromUpdate(e.threadUpdate);
      setSummaries((cur) => mergeSummary(cur, s));
      setOpen((cur) => (cur && cur.channelId === s.threadId ? { ...cur, name: s.title, thread: s.info, archivedAtMs: s.archived ? cur.archivedAtMs || Date.now() : undefined } : cur));
      setFollowedList((cur) => cur.map((t) => (t.channelId === s.threadId ? { ...t, name: s.title, thread: s.info } : t)));
    },
    [],
  );

  const byRoot = useMemo(() => threadsByRoot(channelId ?? "", summaries), [channelId, summaries]);
  const followed = useMemo(() => followedOrder(followedList, unreadByChannel), [followedList, unreadByChannel]);

  return { byRoot, followed, open, openThread, openById, close, reload, applyEvent };
}
