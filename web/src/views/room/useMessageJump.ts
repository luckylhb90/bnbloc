import { useEffect, useRef, useState } from "react";
import { messageJumps, taskSeq, type MessageJump } from "@/lib/team";
import { useToast } from "@/ui/Toast";
import type { FeedItem } from "@/views/room/types";

// Upper bound of history pages paged through to reach a jump target, so a
// stale target in a huge room cannot page forever.
const MAX_JUMP_PAGES = 40;

interface JumpHistory {
  readonly oldestSeq: number | null;
  readonly hasMore: boolean;
  readonly loading: boolean;
  readonly loaded: boolean;
}

// flashMessage scrolls a feed row into view and outlines it briefly. The
// outline never affects layout, so the feed does not shift.
export function flashMessage(seq: number): boolean {
  const el = document.querySelector<HTMLElement>(`[data-room-message-feed] [data-seq="${seq}"]`);
  if (!el) return false;
  el.scrollIntoView({ behavior: "auto", block: "center" });
  el.style.outline = "2px solid var(--color-accent)";
  el.style.outlineOffset = "4px";
  el.style.borderRadius = "var(--radius)";
  window.setTimeout(() => {
    el.style.outline = "";
    el.style.outlineOffset = "";
    el.style.borderRadius = "";
  }, 1600);
  return true;
}

// useMessageJump brings a queued "jump to message" (global search, a task)
// into view: it switches to the message's channel, pages older history until
// the message is loaded, then scrolls to and flashes it. A target that cannot
// be found (deleted, or beyond the paging bound) ends with a notice.
//
// fromQueue=false makes it a private jumper (the people-chat panel): it never
// takes the room's queued jumps, only the ones handed to the returned
// function. While ready=false a target is held (the room is still learning
// which channel is its people chat); divert hands a target elsewhere (true =
// taken, e.g. a people-chat message opens the panel instead).
export function useMessageJump({
  roomId,
  channelId,
  feed,
  history,
  loadEarlier,
  selectChannel,
  fromQueue = true,
  ready = true,
  divert,
}: {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly feed: readonly FeedItem[];
  readonly history: JumpHistory;
  readonly loadEarlier: () => void;
  readonly selectChannel: (channelId: string) => void;
  readonly fromQueue?: boolean;
  readonly ready?: boolean;
  readonly divert?: (jump: MessageJump) => boolean;
}): (jump: MessageJump) => void {
  const toast = useToast();
  const [target, setTarget] = useState<MessageJump | null>(() => (fromQueue ? messageJumps.take(roomId) ?? null : null));
  const pages = useRef(0);

  useEffect(() => {
    if (!fromQueue) return;
    return messageJumps.subscribe((id) => {
      if (id !== roomId) return;
      const next = messageJumps.take(roomId);
      if (next) {
        pages.current = 0;
        setTarget(next);
      }
    });
  }, [roomId, fromQueue]);

  useEffect(() => {
    if (!target || !ready) return;
    if (divert?.(target)) {
      setTarget(null);
      return;
    }
    if (target.channelId && channelId !== target.channelId) {
      selectChannel(target.channelId);
      return;
    }
    if (!channelId || !history.loaded) return;
    if (feed.some((m) => taskSeq(m.seq) === target.seq)) {
      const seq = target.seq;
      setTarget(null);
      window.requestAnimationFrame(() => flashMessage(seq));
      return;
    }
    if (history.loading) return;
    const older = history.oldestSeq != null && history.oldestSeq > target.seq;
    if (history.hasMore && older && pages.current < MAX_JUMP_PAGES) {
      pages.current += 1;
      loadEarlier();
      return;
    }
    setTarget(null);
    toast.info("That message is no longer available");
  }, [target, ready, divert, channelId, feed, history, loadEarlier, selectChannel, toast]);

  return (jump: MessageJump) => {
    pages.current = 0;
    setTarget(jump);
  };
}
