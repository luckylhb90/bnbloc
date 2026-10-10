import { useMemo } from "react";
import type { Room } from "@/lib/api";
import { ConfirmDialog } from "@/ui/Dialog";
import { RoomSettings } from "@/views/RoomSettings";
import { BorrowAuthDialog } from "@/views/BorrowDialog";
import type { RoomAgent } from "@/views/composer/types";
import { MembersDialog } from "@/views/room/MembersDialog";
import type { FeedItem } from "@/views/room/types";
import type { RoomPlatformAgents } from "@/views/room/useRoomPlatformAgents";

export function RoomDialogs({
  roomId,
  room,
  subject,
  online,
  roomAgents,
  platformAgents,
  dialog,
  confirmDelete,
  onCloseDialog,
  onRoomChanged,
  onRoomDeleted,
  onLeave,
  onCloseConfirmDelete,
  onConfirmDelete,
  onQuotaChanged,
}: {
  readonly roomId: string;
  readonly room: Room | null;
  readonly subject: string | null;
  readonly online: string[];
  readonly roomAgents: RoomAgent[];
  readonly platformAgents: Pick<RoomPlatformAgents, "inRoom" | "remove">;
  readonly dialog: "settings" | "members" | "borrow" | "leave" | null;
  readonly confirmDelete: FeedItem | null;
  readonly onCloseDialog: () => void;
  readonly onRoomChanged: (room: Room) => void;
  readonly onRoomDeleted: () => void;
  readonly onLeave: () => void;
  readonly onCloseConfirmDelete: () => void;
  readonly onConfirmDelete: (item: FeedItem) => void;
  readonly onQuotaChanged?: () => void;
}) {
  // My own advertised agents are what I can authorize. One stable array per
  // roster change: the borrow dialog loads authorization state keyed on it.
  const myAgents = useMemo(
    () => roomAgents.filter((a) => a.self).map((a) => ({ core: a.core, provider: a.provider })),
    [roomAgents],
  );
  return (
    <>
      {dialog === "settings" && room && (
        <RoomSettings room={room} onClose={onCloseDialog} onChanged={onRoomChanged} onDeleted={onRoomDeleted} />
      )}
      {dialog === "members" && (
        <MembersDialog
          roomId={roomId}
          online={online}
          roomAgents={roomAgents}
          platformAgents={platformAgents}
          subject={subject}
          onClose={onCloseDialog}
        />
      )}
      {dialog === "borrow" && (
        <BorrowAuthDialog
          roomId={roomId}
          roomName={room?.name || roomId}
          subject={subject}
          knownAgents={myAgents}
          onClose={onCloseDialog}
          onQuotaChanged={onQuotaChanged}
        />
      )}
      <ConfirmDialog
        open={dialog === "leave"}
        onClose={onCloseDialog}
        onConfirm={onLeave}
        title="Leave this room?"
        description="You can rejoin later if it is shared or you have an invite."
        confirmLabel="Leave room"
        danger
      />
      <ConfirmDialog
        open={confirmDelete !== null}
        onClose={onCloseConfirmDelete}
        onConfirm={() => {
          if (confirmDelete) onConfirmDelete(confirmDelete);
        }}
        title="Delete this message?"
        description="It will be removed for everyone in the room. This cannot be undone."
        confirmLabel="Delete"
        danger
      />
    </>
  );
}
