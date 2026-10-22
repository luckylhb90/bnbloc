import { ArrowLeftIcon, GlobeIcon, LockIcon, UsersIcon } from "@phosphor-icons/react";
import type { RoomPreview } from "@/lib/wire.gen";
import { roomLinkAudience } from "@/lib/roomAccess";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { JoinPanel } from "@/views/room/JoinPanel";

// RoomJoinCard is all a person sees of a room they may not read before
// joining (its owner did not turn "readable before joining" on): its card and
// the way to join. Nothing of the room is requested for it.
export function RoomJoinCard({
  room,
  needsCode,
  joining,
  onJoin,
  onBack,
}: {
  readonly room: RoomPreview;
  readonly needsCode: boolean;
  readonly joining: boolean;
  readonly onJoin: (code?: string) => void;
  readonly onBack: () => void;
}) {
  const shared = room.visibility === "shared";
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="pad-safe pad-safe-t flex items-center gap-2 border-b border-[var(--color-border)] px-4 py-3 [--pad-safe-x:1rem] [--pad-safe-y:0.75rem]">
        <Button variant="ghost" size="icon" onClick={onBack} title="Back to rooms" aria-label="Back to rooms" className="tap">
          <ArrowLeftIcon size={18} />
        </Button>
        <h1 className="truncate font-semibold tracking-tight">{room.name || room.roomId}</h1>
      </div>
      <div className="grid flex-1 place-items-center p-6">
        <Card className="flex w-full max-w-md flex-col gap-4 p-6">
          <div className="flex items-center gap-2 text-xs text-[var(--color-fg-subtle)]">
            {shared ? <GlobeIcon size={14} /> : <LockIcon size={14} />}
            <span>{shared ? "Shared room" : "Private room"}</span>
            <span className="inline-flex items-center gap-1 tabular">
              <UsersIcon size={13} />
              {room.memberCount}
            </span>
          </div>
          <div>
            <h2 className="text-lg font-semibold text-[var(--color-fg)]">{room.name || room.roomId}</h2>
            <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{room.description || "No description"}</p>
          </div>
          <p className="text-sm text-[var(--color-fg-muted)]">
            {needsCode ? "Join with the room's invite code to see its messages and files." : "Join to see its messages and files."}
          </p>
          <p className="text-xs text-[var(--color-fg-subtle)]">{roomLinkAudience(room.visibility, room.previewable)}</p>
          <JoinPanel needsCode={needsCode} joining={joining} onJoin={onJoin} />
        </Card>
      </div>
    </div>
  );
}
