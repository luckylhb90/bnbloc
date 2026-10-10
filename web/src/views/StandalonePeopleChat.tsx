import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { dedupeMembers } from "@/lib/format";
import { isRoomGoneStatus, reportRoomGone, type RoomAccessEnd } from "@/lib/roomGone";
import { shareComposerRef } from "@/lib/team";
import { useLocalAgent } from "@/lib/useLocalAgent";
import { useToast } from "@/ui/Toast";
import { PeopleChatPanel } from "@/views/room/PeopleChatPanel";
import type { FeedItem } from "@/views/room/types";

// StandalonePeopleChat is the desktop shell's popped-out people chat window
// (#/people-chat/<roomId>): the same panel as the docked one, full window,
// with its own stream. "Ask the agent about this" reaches the main window's
// room composer through the shared-reference hand-off.
export function StandalonePeopleChat({ roomId }: { roomId: string }) {
  const { subject } = useAuth();
  const toast = useToast();
  const agent = useLocalAgent(false);
  const [channelId, setChannelId] = useState<string | null>(null);
  const [members, setMembers] = useState<string[]>([]);
  const [gone, setGone] = useState<RoomAccessEnd | "missing" | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .openPeopleChat(roomId)
      .then((c) => {
        if (!cancelled) setChannelId(c.channelId);
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        if (e instanceof ApiError && isRoomGoneStatus(e.status)) {
          reportRoomGone(roomId);
          setGone("missing");
        }
      });
    api
      .getRoom(roomId)
      .then((r) => {
        if (!cancelled) document.title = `People chat · ${r.name || roomId}`;
      })
      .catch(() => {});
    api
      .members(roomId)
      .then((m) => {
        if (!cancelled) setMembers(dedupeMembers((m.members || []).map((x) => x.userId)));
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [roomId]);

  const askAgent = useCallback(
    (item: FeedItem) => {
      shareComposerRef(roomId, { id: item.id, author: item.author, text: item.text });
      toast.info("Added to the room composer in the main window");
    },
    [roomId, toast],
  );
  const onGone = useCallback(
    (reason: RoomAccessEnd) => {
      reportRoomGone(roomId);
      setGone(reason);
    },
    [roomId],
  );

  if (gone) {
    return (
      <div className="grid h-[100dvh] place-items-center bg-[var(--color-bg)] p-6 text-sm text-[var(--color-fg-muted)]">
        {gone === "deleted"
          ? "This room was deleted."
          : gone === "removed"
            ? "You were removed from this room."
            : "This room no longer exists or you no longer have access."}
      </div>
    );
  }
  return (
    <div className="coarse-targets flex h-[100dvh] flex-col bg-[var(--color-bg)]">
      {channelId && (
        <PeopleChatPanel
          roomId={roomId}
          channelId={channelId}
          subject={subject}
          joinedGen={0}
          agent={agent}
          members={members}
          online={[]}
          jumpSeq={null}
          onJumped={() => {}}
          onAskAgent={askAgent}
          onGone={onGone}
        />
      )}
    </div>
  );
}
