import { useCallback, useMemo, useState, type ReactNode } from "react";
import type { Channel, RoomDetail } from "@/lib/api";
import { channelsApi } from "@/lib/channelsApi";
import { reorderedPositions } from "@/lib/channelRules";
import { errorText } from "@/lib/errors";
import type { RoomMembership } from "@/lib/roomRoles";
import type { RoomSection } from "@/lib/wire.gen";
import { ConfirmDialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";
import type { RoomAgent } from "@/views/composer/types";
import type { ChannelActions } from "@/views/room/ChannelMenu";
import { ChannelSettingsDialog, CreateChannelDialog, SectionDialog } from "@/views/room/ChannelDialogs";
import { ArchivedChannelsDialog, ChannelDirectoryDialog } from "@/views/room/ChannelDirectory";
import type { RoomChannels } from "@/views/room/useRoomChannels";

type Open =
  | { readonly kind: "create" }
  | { readonly kind: "settings"; readonly channel: Channel }
  | { readonly kind: "section"; readonly section: RoomSection | null }
  | { readonly kind: "directory" }
  | { readonly kind: "archived" }
  | { readonly kind: "delete"; readonly channel: Channel }
  | null;

// useChannelManager owns what a member does to the room's channels and
// sections from the rail (docs/prd/2026-10-28-channels-and-threads.md §8
// slice 1): the calls (the room service decides; lib/channelRules only
// decides what is offered), the dialogs, and what each action said. A
// refusal is shown on the control that acted: in its dialog, or on the
// channel's row for a menu action, in the server's words. Every change
// reloads the listing (and the room's channel_updated event reloads it for
// everyone else).
export function useChannelManager({
  roomId,
  subject,
  me,
  room,
  channels,
  roomAgents,
  onSelect,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly me: RoomMembership | null;
  readonly room: RoomDetail | null;
  readonly channels: RoomChannels;
  readonly roomAgents: readonly RoomAgent[];
  readonly onSelect: (channelId: string) => void;
}): { readonly actions: ChannelActions; readonly dialogs: ReactNode } {
  const toast = useToast();
  const [open, setOpen] = useState<Open>(null);
  const [notes, setNotes] = useState<Record<string, string>>({});
  const { reload } = channels;

  // act runs a menu action of a channel: its outcome is said on its row.
  const act = useCallback(
    async (c: Channel, what: string, run: () => Promise<unknown>, done?: string) => {
      setNotes({});
      try {
        await run();
        await reload();
        if (done) toast.success(done);
      } catch (e) {
        setNotes({ [c.channelId]: errorText(e, `Could not ${what}`) });
      }
    },
    [reload, toast],
  );

  const actions = useMemo<ChannelActions>(
    () => ({
      me,
      subject,
      create: () => setOpen({ kind: "create" }),
      createSection: () => setOpen({ kind: "section", section: null }),
      browse: () => setOpen({ kind: "directory" }),
      showArchived: () => setOpen({ kind: "archived" }),
      settings: (c) => setOpen({ kind: "settings", channel: c }),
      editSection: (s) => setOpen({ kind: "section", section: s }),
      move: (c, siblings, step) =>
        void act(c, "move it", async () => {
          for (const p of reorderedPositions(siblings, c.channelId, step)) await channelsApi.patch(roomId, p.channelId, { position: p.position });
        }),
      mark: (c, marks) => void act(c, marks.hidden ? "hide it" : "star it", () => channelsApi.follow(roomId, c.channelId, marks), marks.hidden ? `#${c.name} is hidden: find it in Browse channels` : undefined),
      leave: (c) => void act(c, "leave it", () => channelsApi.unfollow(roomId, c.channelId), `You left #${c.name}: find it in Browse channels`),
      archive: (c, archived) =>
        void act(c, archived ? "archive it" : "restore it", () => channelsApi.archive(roomId, c.channelId, archived), archived ? `Archived #${c.name}: it is read-only, under Archived` : `Restored #${c.name}`),
      remove: (c) => setOpen({ kind: "delete", channel: c }),
      note: (id) => notes[id] ?? "",
    }),
    [me, subject, roomId, act, notes],
  );

  const close = () => setOpen(null);
  let dialogs: ReactNode = null;
  if (open?.kind === "create") {
    dialogs = (
      <CreateChannelDialog
        roomId={roomId}
        subject={subject}
        me={me}
        policy={room?.channelCreation ?? ""}
        sections={channels.sections}
        onClose={close}
        onCreated={(c) => {
          close();
          void reload().then(() => onSelect(c.channelId));
        }}
      />
    );
  } else if (open?.kind === "settings") {
    dialogs = (
      <ChannelSettingsDialog
        roomId={roomId}
        subject={subject}
        me={me}
        policy={room?.channelCreation ?? ""}
        channel={open.channel}
        sections={channels.sections}
        roomAgents={roomAgents}
        onClose={close}
        onSaved={() => {
          close();
          void reload();
        }}
      />
    );
  } else if (open?.kind === "section") {
    dialogs = (
      <SectionDialog
        roomId={roomId}
        section={open.section}
        sections={channels.sections}
        onClose={close}
        onSaved={() => {
          close();
          void reload();
        }}
      />
    );
  } else if (open?.kind === "directory") {
    dialogs = <ChannelDirectoryDialog roomId={roomId} onClose={close} onOpen={(id) => { close(); onSelect(id); }} onChanged={() => void reload()} />;
  } else if (open?.kind === "archived") {
    dialogs = (
      <ArchivedChannelsDialog
        roomId={roomId}
        subject={subject}
        me={me}
        sections={channels.sections}
        onClose={close}
        onOpen={(id) => { close(); onSelect(id); }}
        onChanged={() => void reload()}
      />
    );
  } else if (open?.kind === "delete") {
    const c = open.channel;
    dialogs = (
      <ConfirmDialog
        open
        danger
        title={`Delete #${c.name}?`}
        description="Its messages leave the room for every member. Archive it instead to keep its history readable."
        confirmLabel="Delete channel"
        onClose={close}
        onConfirm={() => act(c, "delete it", () => channelsApi.remove(roomId, c.channelId), `Deleted #${c.name}`)}
      />
    );
  }
  return { actions, dialogs };
}
