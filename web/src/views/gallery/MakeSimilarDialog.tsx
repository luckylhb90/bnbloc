import { useEffect, useState } from "react";
import { MagicWandIcon } from "@phosphor-icons/react";
import { useAuth } from "@/lib/auth";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { galleryErrorText, galleryRoomRequest, queueSimilarDraft, type MakeSimilarDraft } from "@/lib/workGallery";
import { workGalleryApi } from "@/lib/workGalleryApi";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { GalleryRoomField, useGalleryRoom } from "./WorkParts";

// MakeSimilarDialog is a work's "Make similar"
// (docs/prd/2026-10-13-works-gallery-and-make-similar.md §4): the member
// picks a room (a new one named after the work by default, or one they may
// post in); the server copies the recipe's reference files in as their
// attachments, turns on the packs they may see and answers a draft, which
// goes to the room's composer through the injected-draft owner
// (queueSimilarDraft: the text, the expert, the attachments). The member
// edits it and sends it with their own agent. What the server left out is
// listed before the room opens.

export function MakeSimilarDialog({
  work,
  onClose,
  onDrafted,
}: {
  readonly work: ContentComponent | null;
  readonly onClose: () => void;
  readonly onDrafted: (roomId: string) => void;
}) {
  const { reauth } = useAuth();
  const toast = useToast();
  const label = work ? contentLabel(work) : "";
  const room = useGalleryRoom(!!work, label);
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<MakeSimilarDraft | null>(null);
  useEffect(() => {
    setDone(null);
    setFailure("");
  }, [work]);

  if (!work) return null;
  const submit = async () => {
    if (room.problem || busy) return;
    setBusy(true);
    setFailure("");
    try {
      const draft = await workGalleryApi.makeSimilar(work.id, galleryRoomRequest(room.target, work.version));
      // A new room is the member's: refresh their room scope before opening it.
      if (draft.createdRoom) await reauth().catch(() => {});
      queueSimilarDraft(draft);
      if ((draft.skipped ?? []).length > 0) {
        setDone(draft);
        return;
      }
      toast.success("Your draft is in the room: change it as you like, then send it");
      onDrafted(draft.roomId);
    } catch (e) {
      setFailure(galleryErrorText(e, "similar"));
    } finally {
      setBusy(false);
    }
  };

  if (done) {
    return (
      <Dialog
        open
        onClose={() => onDrafted(done.roomId)}
        title="Your draft is ready"
        description="The room opens with the request, its expert and its files in the composer. Some of the recipe was left out:"
        footer={
          <Button variant="primary" onClick={() => onDrafted(done.roomId)}>
            Open the room
          </Button>
        }
      >
        <ul className="flex list-disc flex-col gap-1 pl-5 text-sm text-[var(--color-fg-muted)]" aria-label="Left out">
          {(done.skipped ?? []).map((s) => (
            <li key={s}>{s}</li>
          ))}
        </ul>
      </Dialog>
    );
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title={`Make something like ${label}`}
      description="The room opens with this work's recipe in the composer: its request, expert and files, with its skills turned on. Change it as you like, then send it to your own agent."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!!room.problem || busy}>
            <MagicWandIcon size={15} />
            {busy ? "Preparing…" : "Make similar"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <GalleryRoomField room={room} roomHint="A room you can post in: the draft waits in its composer, and nothing is sent until you do." />
        {failure && <ErrorBox>{failure}</ErrorBox>}
      </div>
    </Dialog>
  );
}
