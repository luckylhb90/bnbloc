import { LinkSimpleIcon } from "@phosphor-icons/react";
import { appLinkBase } from "@/lib/config";
import { roomLinkUrl } from "@/lib/hashRoute";
import { roomLinkAudience } from "@/lib/roomAccess";
import type { RoomVisibility } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { useCopy } from "@/ui/useCopy";

// RoomLinkButton copies the room's link (the address to send someone), and
// says on hover who it opens for.
export function RoomLinkButton({
  roomId,
  visibility,
  previewable,
}: {
  readonly roomId: string;
  readonly visibility: RoomVisibility;
  readonly previewable: boolean;
}) {
  const copy = useCopy();
  const audience = roomLinkAudience(visibility, previewable);
  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={() => void copy(roomLinkUrl(appLinkBase(), roomId), "room link")}
      title={`Copy room link. ${audience}`}
      aria-label="Copy room link"
      className="tap"
    >
      <LinkSimpleIcon size={18} />
    </Button>
  );
}
