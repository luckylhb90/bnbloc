import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { messageJumps, type MessageJump } from "@/lib/team";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

export interface PeopleChat {
  // The room's people chat channel; null until it exists (a member opens it).
  readonly channelId: string | null;
  // The first lookup landed: queued jumps may now be routed.
  readonly resolved: boolean;
  readonly open: boolean;
  // A message the panel should scroll to once its history is loaded.
  readonly jumpSeq: number | null;
  readonly openPanel: (seq?: number) => void;
  readonly closePanel: () => void;
  readonly clearJump: () => void;
  // useMessageJump's divert: a jump into the people chat opens the panel at
  // that message instead of switching the main feed's channel.
  readonly divertJump: (jump: MessageJump) => boolean;
}

// usePeopleChat owns a room's people-chat entry point: which channel it is
// (read without creating it — the channel is created on first open), whether
// its panel is open, and the routing of deep links / search hits / tasks that
// point into it.
export function usePeopleChat({
  roomId,
  joinedGen,
  enabled,
  unreadByChannel,
}: {
  readonly roomId: string;
  readonly joinedGen: number;
  // False for a room preview (not a member): there is no people chat to
  // read. Null while the room itself is still loading (jumps wait).
  readonly enabled: boolean | null;
  // The room's per-channel unread. A people chat another member created after
  // this room was opened shows up here first, under an id we do not know yet.
  readonly unreadByChannel: Record<string, number>;
}): PeopleChat {
  const toast = useToast();
  const [channelId, setChannelId] = useState<string | null>(null);
  const [resolved, setResolved] = useState(false);
  const [open, setOpen] = useState(false);
  const [jumpSeq, setJumpSeq] = useState<number | null>(null);

  const lookup = useCallback(
    () =>
      api.peopleChat(roomId).then(
        (c) => c.channelId,
        () => null,
      ),
    [roomId],
  );

  useEffect(() => {
    if (enabled === null) return;
    if (!enabled) {
      setResolved(true);
      return;
    }
    let cancelled = false;
    void lookup().then((id) => {
      if (cancelled) return;
      if (id) setChannelId(id);
      setResolved(true);
    });
    return () => {
      cancelled = true;
    };
  }, [enabled, lookup, joinedGen]);

  // Unread in a channel we cannot place while we know no people chat: it may
  // be one created since — look again (only while unknown).
  const unreadKeys = Object.keys(unreadByChannel).sort().join(",");
  useEffect(() => {
    if (!enabled || !resolved || channelId || !unreadKeys) return;
    let cancelled = false;
    void lookup().then((id) => {
      if (!cancelled && id) setChannelId(id);
    });
    return () => {
      cancelled = true;
    };
  }, [enabled, resolved, channelId, unreadKeys, lookup]);

  const openPanel = useCallback(
    (seq?: number) => {
      if (seq) setJumpSeq(seq);
      if (channelId) {
        setOpen(true);
        return;
      }
      api
        .openPeopleChat(roomId)
        .then((c) => {
          setChannelId(c.channelId);
          setOpen(true);
        })
        .catch((e: unknown) => {
          toast.error(errorText(e, "Could not open the people chat"));
        });
    },
    [channelId, roomId, toast],
  );
  const closePanel = useCallback(() => setOpen(false), []);
  const clearJump = useCallback(() => setJumpSeq(null), []);

  // Channels already checked against the server for this room visit, so a
  // re-queued jump that is not ours falls through to the main feed.
  const checked = useRef(new Set<string>());
  const divertJump = useCallback(
    (jump: MessageJump): boolean => {
      const target = jump.channelId;
      if (!target) return false;
      if (channelId) {
        if (target !== channelId) return false;
        openPanel(jump.seq);
        return true;
      }
      if (checked.current.has(target)) return false;
      checked.current.add(target);
      // We know no people chat yet; it may exist since. Ask, then route.
      void lookup().then((id) => {
        if (id) setChannelId(id);
        if (id === target) {
          setJumpSeq(jump.seq);
          setOpen(true);
        } else {
          messageJumps.queue(roomId, jump);
        }
      });
      return true;
    },
    [channelId, openPanel, lookup, roomId],
  );

  return { channelId, resolved, open, jumpSeq, openPanel, closePanel, clearJump, divertJump };
}
