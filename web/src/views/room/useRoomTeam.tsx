import { useEffect, useMemo, useState } from "react";
import type { RoomEvent } from "@/lib/roomEvents";
import { rememberLastRoom } from "@/lib/team";
import { useAuth } from "@/lib/auth";
import type { PromptDraft } from "@/lib/promptPacks";
import { PromptPackEditorDialog, type PromptPackTarget } from "@/views/library/PromptPackEditorDialog";
import type { MessageExtras } from "@/views/room/messageExtras";
import { RoomTasksStrip } from "@/views/room/RoomTasksStrip";
import { PinnedStrip } from "@/views/room/MessageMarks";
import { ForwardDialog } from "@/views/room/Forward";
import { CatchUpPill, useTranslations } from "@/views/room/CatchUp";
import type { ForwardRef } from "@/lib/wire.gen";
import { SavePromptDialog, type PromptSource } from "@/views/room/SavePromptDialog";
import type { FeedItem } from "@/views/room/types";
import { useMessageJump } from "@/views/room/useMessageJump";
import { useRoomMarks } from "@/views/room/useRoomMarks";
import { useRoomTasks } from "@/views/room/useRoomTasks";

// useRoomTeam wires the team features into one room view: message tasks
// (strip + badges + live task_updated), reactions, pins and saved messages
// (live message_marked), "Save as prompt", and jump-to-message
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
  unreadFromSeq = null,
  channelName = "",
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
  // The open channel's first unread message (the catch-up reads from it) and
  // its name.
  readonly unreadFromSeq?: number | null;
  readonly channelName?: string;
}) {
  const tasks = useRoomTasks(roomId, joinedGen);
  const jump = useMessageJump({ roomId, channelId, feed, history, loadEarlier, selectChannel, ready: jumpReady, divert: divertJump });
  const [saving, setSaving] = useState<PromptSource | null>(null);
  // Several messages saved as prompts continue in the prompt pack form.
  const [packing, setPacking] = useState<PromptPackTarget | null>(null);
  // The message being forwarded (§2.3), from this feed's channel.
  const [forwarding, setForwarding] = useState<ForwardRef | null>(null);

  const { subject } = useAuth();
  const marks = useRoomMarks(roomId, joinedGen, subject ?? null);
  const translations = useTranslations(roomId, channelId);
  useEffect(() => {
    if (subject) rememberLastRoom(subject, roomId);
  }, [subject, roomId]);

  const { bySeq, mark, unmark, setStatus } = tasks;
  const extras = useMemo<MessageExtras>(
    () => ({
      taskBySeq: bySeq,
      onSaveAsPrompt: (item) => setSaving({ text: item.text, seq: item.seq }),
      onMarkTask: mark,
      onUnmarkTask: unmark,
      onSetTaskStatus: setStatus,
      marks: marks.marks,
      me: subject ?? null,
      onSetMark: marks.setMark,
      onForward: (seq: number) => setForwarding({ roomId, seq, ...(channelId ? { channelId } : {}) }),
      translations,
    }),
    [bySeq, mark, unmark, setStatus, marks.marks, marks.setMark, subject, roomId, channelId, translations],
  );

  const strip = (
    <>
      <CatchUpPill roomId={roomId} channelId={channelId} channelName={channelName} me={subject ?? null} feed={feed} unreadFromSeq={unreadFromSeq} />
      <PinnedStrip marks={marks.marks} channelId={channelId} feed={feed} onJump={(seq, ch) => jump({ seq, channelId: ch || undefined })} />
      <RoomTasksStrip
        tasks={tasks.tasks}
        onSetStatus={setStatus}
        onJump={(t) => jump({ seq: t.seq, channelId: t.channelId || undefined })}
      />
    </>
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
      <ForwardDialog source={forwarding} onClose={() => setForwarding(null)} />
    </>
  );
  const applyEvent = (e: RoomEvent) => tasks.applyEvent(e) || marks.applyEvent(e);

  return { extras, strip, dialogs, applyEvent };
}
