import { useState } from "react";
import { CopySimpleIcon } from "@phosphor-icons/react";
import { useAuth } from "@/lib/auth";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { galleryErrorText, galleryRoomRequest } from "@/lib/workGallery";
import { workGalleryApi } from "@/lib/workGalleryApi";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { GalleryRoomField, useGalleryRoom } from "./WorkParts";

// UseAppDialog is an app's "Use this app" (docs/prd/2026-10-13-app-gallery.md
// §8): a copy of the app's site as a new deliverable in a room the member
// owns (one they may post in) or in a new room named after the app, with a
// data space of its own that starts empty. The room choice is the
// gallery's (useGalleryRoom); the server decides (a room the member does
// not own, an app they no longer see: galleryErrorText). On success the
// room opens with the copy.

export function UseAppDialog({
  app,
  onClose,
  onUsed,
}: {
  readonly app: ContentComponent | null;
  readonly onClose: () => void;
  readonly onUsed: (roomId: string, artifactId: string) => void;
}) {
  const { reauth } = useAuth();
  const toast = useToast();
  const label = app ? contentLabel(app) : "";
  const room = useGalleryRoom(!!app, label);
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);

  if (!app) return null;
  const submit = async () => {
    if (room.problem || busy) return;
    setBusy(true);
    setFailure("");
    try {
      const reply = await workGalleryApi.use(app.id, galleryRoomRequest(room.target, app.version));
      // A new room is the member's: refresh their room scope before opening it.
      if (reply.createdRoom) await reauth().catch(() => {});
      toast.success(`${label} is in ${reply.createdRoom ? "a new room" : "the room"}, with its own empty data`);
      onUsed(reply.roomId, reply.artifactId);
    } catch (e) {
      setFailure(galleryErrorText(e, "use"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={`Use ${label}`}
      description="You get your own copy of the app as a new deliverable, with its own empty data. Ask an agent in the room to change it as you like; the original stays as it is."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!!room.problem || busy}>
            <CopySimpleIcon size={15} />
            {busy ? "Copying…" : "Use this app"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <GalleryRoomField room={room} roomHint="A room you can post in: the copy is its owners' to change and to share." />
        {failure && <ErrorBox>{failure}</ErrorBox>}
      </div>
    </Dialog>
  );
}
