import { useEffect, useMemo, useState } from "react";
import type { RoomEvent } from "@/lib/roomEvents";
import { rememberLastRoom } from "@/lib/team";
import { useAuth } from "@/lib/auth";
import type { PromptDraft } from "@/lib/promptPacks";
import { PromptPackEditorDialog, type PromptPackTarget } from "@/views/library/PromptPackEditorDialog";
import type { MessageExtras } from "@/views/room/messageExtras";
import { RoomTasksStrip } from "@/views/room/RoomTasksStrip";
import { SavePromptDialog, type PromptSource } from "@/views/room/SavePromptDialog";
import { SaveToMemoryDialog, type MemorySource } from "@/views/room/SaveToMemoryDialog";
import type { FeedItem } from "@/views/room/types";
import { useMessageJump } from "@/views/room/useMessageJump";
import { useRoomTasks } from "@/views/room/useRoomTasks";

// useRoomTeam wires the team features into one room view: message tasks
// (strip + badges + live task_updated), "Save as prompt", "Save to memory",
// and jump-to-message
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
  mayAct,
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
  // The member may act in the room (lib/roomRoles mayChat): only then is a
  // message offered "Save to memory".
  readonly mayAct: boolean;
}) {
  const tasks = useRoomTasks(roomId, joinedGen);
  const jump = useMessageJump({ roomId, channelId, feed, history, loadEarlier, selectChannel, ready: jumpReady, divert: divertJump });
  const [saving, setSaving] = useState<PromptSource | null>(null);
  const [memorizing, setMemorizing] = useState<MemorySource | null>(null);
  // Several messages saved as prompts continue in the prompt pack form.
  const [packing, setPacking] = useState<PromptPackTarget | null>(null);

  const { subject } = useAuth();
  useEffect(() => {
    if (subject) rememberLastRoom(subject, roomId);
  }, [subject, roomId]);

  const { bySeq, mark, unmark, setStatus } = tasks;
  const extras = useMemo<MessageExtras>(
    () => ({
      taskBySeq: bySeq,
      onSaveAsPrompt: (item) => setSaving({ text: item.text, seq: item.seq }),
      onSaveToMemory: mayAct ? (item) => item.seq != null && setMemorizing({ text: item.text, seq: item.seq }) : undefined,
      onMarkTask: mark,
      onUnmarkTask: unmark,
      onSetTaskStatus: setStatus,
    }),
    [bySeq, mark, unmark, setStatus, mayAct],
  );

  const strip = (
    <RoomTasksStrip
      tasks={tasks.tasks}
      onSetStatus={setStatus}
      onJump={(t) => jump({ seq: t.seq, channelId: t.channelId || undefined })}
    />
  );
  const dialogs = (
    <>
      <SavePromptDialog
        source={saving}
        feed={feed}
        onClose={() => setSaving(null)}
        onContinue={(drafts: PromptDraft[]) => setPacking({ drafts })}
      />
      <PromptPackEditorDialog target={packing} onClose={() => setPacking(null)} />
      <SaveToMemoryDialog roomId={roomId} source={memorizing} onClose={() => setMemorizing(null)} />
    </>
  );
  const applyEvent = (e: RoomEvent) => tasks.applyEvent(e);

  return { extras, strip, dialogs, applyEvent };
}
