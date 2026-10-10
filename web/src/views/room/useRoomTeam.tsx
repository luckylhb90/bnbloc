import { useEffect, useMemo, useState } from "react";
import type { RoomEvent } from "@/lib/sse";
import { rememberLastRoom } from "@/lib/team";
import { PromptEditorDialog, type PromptSource } from "@/views/library/PromptDialogs";
import type { MessageExtras } from "@/views/room/messageExtras";
import { RoomTasksStrip } from "@/views/room/RoomTasksStrip";
import type { FeedItem } from "@/views/room/types";
import { useMessageJump } from "@/views/room/useMessageJump";
import { useRoomTasks } from "@/views/room/useRoomTasks";

// useRoomTeam wires the team features into one room view: message tasks
// (strip + badges + live task_updated), "Save to library", and jump-to-message
// (from global search or a task). RoomView renders `strip` over the feed,
// provides `extras` to the feed rows, mounts `dialogs`, and routes stream
// events through `applyEvent` first.
export function useRoomTeam({
  roomId,
  channelId,
  joinedGen,
  feed,
  history,
  loadEarlier,
  selectChannel,
  jumpReady,
  divertJump,
}: {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly joinedGen: number;
  readonly feed: readonly FeedItem[];
  readonly history: Parameters<typeof useMessageJump>[0]["history"];
  readonly loadEarlier: () => void;
  readonly selectChannel: (channelId: string) => void;
  // See useMessageJump: jumps wait until the room knows its people chat, and
  // a jump into the people chat opens the panel instead of the main feed.
  readonly jumpReady?: boolean;
  readonly divertJump?: Parameters<typeof useMessageJump>[0]["divert"];
}) {
  const tasks = useRoomTasks(roomId, joinedGen);
  const jump = useMessageJump({ roomId, channelId, feed, history, loadEarlier, selectChannel, ready: jumpReady, divert: divertJump });
  const [saving, setSaving] = useState<PromptSource | null>(null);

  useEffect(() => rememberLastRoom(roomId), [roomId]);

  const { bySeq, mark, unmark, setStatus } = tasks;
  const extras = useMemo<MessageExtras>(
    () => ({
      taskBySeq: bySeq,
      onSaveToLibrary: (item) => setSaving({ text: item.text, roomId, seq: item.seq }),
      onMarkTask: mark,
      onUnmarkTask: unmark,
      onSetTaskStatus: setStatus,
    }),
    [bySeq, mark, unmark, setStatus, roomId],
  );

  const strip = (
    <RoomTasksStrip
      tasks={tasks.tasks}
      onSetStatus={setStatus}
      onJump={(t) => jump({ seq: t.seq, channelId: t.channelId || undefined })}
    />
  );
  const dialogs = <PromptEditorDialog source={saving} onClose={() => setSaving(null)} />;
  const applyEvent = (e: RoomEvent) => tasks.applyEvent(e);

  return { extras, strip, dialogs, applyEvent };
}
