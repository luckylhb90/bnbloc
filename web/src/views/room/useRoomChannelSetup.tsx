import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { Room, RoomDetail } from "@/lib/api";
import { isPreviewRoom } from "@/lib/apiModels";
import { sectionOf } from "@/lib/channelRules";
import { markWelcomeSeen, welcomeSeen } from "@/lib/roomMemory";
import type { RoomMembership } from "@/lib/roomRoles";
import type { RoomSection } from "@/lib/wire.gen";
import type { RoomAgent } from "@/views/composer/types";
import type { ChannelActions } from "@/views/room/ChannelMenu";
import { ChannelHeader } from "@/views/room/ChannelHeader";
import { hasWelcome, RoomWelcomePage } from "@/views/room/RoomWelcomePage";
import { useChannelGate, type ChannelGate } from "@/views/room/useChannelGate";
import { useChannelManager } from "@/views/room/useChannelManager";
import { useRoomChannels, type RoomChannels } from "@/views/room/useRoomChannels";

export interface RoomChannelSetup {
  readonly channels: RoomChannels;
  readonly actions: ChannelActions;
  readonly gate: ChannelGate;
  readonly header: ReactNode;
  readonly dialogs: ReactNode;
  readonly openWelcome: (() => void) | undefined;
  readonly section: RoomSection | null;
}

// useRoomChannelSetup is the room view's channel side (docs/prd/2026-10-28-
// channels-and-threads.md, slice 1): the listing (reloaded on
// channel_updated, `onEvent`), the rail's actions and their dialogs, the
// open channel's header, the composer's gate, and the room's welcome page
// (shown once on a member's first open of a room that has one, and from the
// header after).
export function useRoomChannelSetup({
  roomId,
  subject,
  room,
  me,
  roomAgents,
  channelId,
  epoch,
  onSelect,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly room: Room | null;
  readonly me: RoomMembership | null;
  readonly roomAgents: readonly RoomAgent[];
  readonly channelId: string | null;
  readonly epoch: number;
  readonly onSelect: (channelId: string) => void;
}): RoomChannelSetup {
  const channels = useRoomChannels(roomId, epoch, channelId, onSelect);
  const detail: RoomDetail | null = room && !isPreviewRoom(room) ? room : null;
  const { actions, dialogs: manageDialogs } = useChannelManager({ roomId, subject, me, room: detail, channels, roomAgents, onSelect });
  const gate = useChannelGate({ roomId, channel: channels.current, me });
  const section = channels.current ? sectionOf(channels.current, channels.sections) : null;

  const [welcomeOpen, setWelcomeOpen] = useState(false);
  // A member's first open of a room with a welcome page shows it once.
  const showable = !!detail && !!me && hasWelcome(detail);
  useEffect(() => {
    if (showable && !welcomeSeen(subject, roomId)) {
      markWelcomeSeen(subject, roomId);
      setWelcomeOpen(true);
    }
  }, [showable, subject, roomId]);
  const closeWelcome = useCallback(() => setWelcomeOpen(false), []);

  const dialogs = (
    <>
      {manageDialogs}
      {welcomeOpen && detail && (
        <RoomWelcomePage
          room={detail}
          channels={channels.channels ?? []}
          sections={channels.sections}
          onClose={closeWelcome}
          onOpen={(id) => {
            closeWelcome();
            onSelect(id);
          }}
          onChanged={() => void channels.reload()}
        />
      )}
    </>
  );
  return {
    channels,
    actions,
    gate,
    header: <ChannelHeader channel={channels.current} section={section} room={detail} />,
    dialogs,
    openWelcome: detail && me ? () => setWelcomeOpen(true) : undefined,
    section,
  };
}
