import { useEffect } from "react";
import { contentApi } from "@/lib/contentComponentsApi";
import { contentSaves, isOwnSave, submittedMessage } from "@/lib/contentSavedEvents";
import { useToast } from "@/ui/Toast";
import { savedActions, savedMessage } from "@/views/library/workshopFlow";

// useWorkshopSaves announces what the room tools saved for the member while
// they work in a room (the expert workshop, or any room an agent saves
// from): the gateway publishes a transient content_saved room event on each
// save or publish request it makes on behalf of a job's requester, and the
// member it was saved for gets a toast with "Open in Library" and "Try it
// now". Nothing polls the Library.
export function useWorkshopSaves(roomId: string, subject: string | null): void {
  const toast = useToast();
  useEffect(
    () =>
      contentSaves.subscribe((room, s) => {
        if (room !== roomId || !isOwnSave(s, subject)) return;
        if (s.action !== "saved") {
          toast.info(submittedMessage(s));
          return;
        }
        // The full component carries what "Try it now" uses (quick prompts,
        // a pack's commands).
        contentApi
          .get(s.componentId)
          .then((d) => toast.success(savedMessage(d.component), savedActions(d.component, roomId, (msg) => toast.info(msg))))
          .catch(() => toast.success(`Saved ${s.displayName || s.name} v${s.version}`));
      }),
    [roomId, subject, toast],
  );
}
