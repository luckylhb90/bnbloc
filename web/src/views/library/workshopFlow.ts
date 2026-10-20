import { useCallback, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { platformAgentsApi } from "@/lib/platformAgentsApi";
import { announcePromptPacksChanged } from "@/lib/promptPacksApi";
import { browserStorage } from "@/lib/storageKeys";
import { composerAgentPicks, composerDrafts, composerExpertPicks, queueLibraryTab } from "@/lib/team";
import { routeHash } from "@/lib/useHashRoute";
import {
  WORKSHOP_ROOM_NAME,
  builderOf,
  cachedWorkshop,
  isBuilder,
  libraryTabOf,
  rememberWorkshop,
  tryItDraft,
  workshopAgent,
  workshopAgentPick,
  workshopCandidates,
  workshopDraft,
  workshopRoomId,
  type WorkshopRequest,
} from "@/lib/workshop";
import type { ToastAction } from "@/ui/Toast";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

// Room ids are global: a taken id is retried with a fresh suffix.
const CREATE_ATTEMPTS = 3;

// pinsBuilder reports whether a room pins an Expert builder (an expert
// named expert-builder: what makes a room the member's workshop). A room
// that cannot be read is not it.
async function pinsBuilder(roomId: string): Promise<boolean> {
  try {
    const uses = await contentApi.uses(roomId);
    return (uses.room ?? []).some((u) => isBuilder(u.component));
  } catch {
    return false;
  }
}

async function createWorkshopRoom(): Promise<string> {
  for (let attempt = 1; ; attempt++) {
    const roomId = workshopRoomId();
    try {
      await api.createRoom({ roomId, name: WORKSHOP_ROOM_NAME, visibility: "private" });
      return roomId;
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 409) || attempt >= CREATE_ATTEMPTS) throw e;
    }
  }
}

// findOrCreateWorkshop returns the member's workshop room: the cached one,
// else an owned private room pinning a builder (rooms named "Expert
// workshop" first), else a new private room with the builder pinned (when
// one is installed).
async function findOrCreateWorkshop(subject: string, reauth: () => Promise<void>, builder: ContentComponent | null): Promise<string> {
  const storage = browserStorage();
  const rooms = (await api.myRooms(subject)).rooms ?? [];
  for (const id of workshopCandidates(rooms, cachedWorkshop(storage, subject))) {
    if (await pinsBuilder(id)) {
      rememberWorkshop(storage, subject, id);
      return id;
    }
  }
  const roomId = await createWorkshopRoom();
  // Refresh the session's room scope so the new room is reachable.
  await reauth().catch(() => {});
  if (builder) await contentApi.putUse("room", builder.id, { roomId });
  rememberWorkshop(storage, subject, roomId);
  return roomId;
}

// builderComponent is the Expert builder of the member's Library, found by
// name (builderOf: the nearest scope's), as the content API lists it (its
// quick prompts are the workshop's prefills), or null when none is
// installed or the Library cannot be read: the workshop still opens, with
// an empty draft and no expert picked.
async function builderComponent(): Promise<ContentComponent | null> {
  try {
    return builderOf((await contentApi.list("expert")).components ?? []);
  } catch {
    return null;
  }
}

// useOpenWorkshop opens the expert workshop with the builder picked, the
// member's own agent (else a platform agent) selected and the builder's
// quick prompt for the request in the composer (workshopDraft). Without any
// agent the room still opens, with guidance.
export function useOpenWorkshop(onOpenRoom: (roomId: string) => void): { readonly open: (req: WorkshopRequest) => Promise<void>; readonly busy: boolean } {
  const { subject, reauth } = useAuth();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const open = useCallback(
    async (req: WorkshopRequest) => {
      if (!subject || busy) return;
      setBusy(true);
      try {
        const builder = await builderComponent();
        const [roomId, offered, own] = await Promise.all([
          findOrCreateWorkshop(subject, reauth, builder),
          platformAgentsApi.available().then((r) => r.agents).catch(() => []),
          api.myAgents().then((r) => r.agents ?? []).catch(() => []),
        ]);
        const pick = workshopAgentPick(workshopAgent(offered, own, subject));
        if (pick && !pick.own) {
          // Idempotent: a platform agent joins the room through a grant.
          await platformAgentsApi.add(roomId, { core: pick.core, provider: pick.provider }).catch(() => {});
        }
        if (pick) composerAgentPicks.queue(roomId, pick);
        else toast.info("Connect an agent to use the builder: install the desktop app or ask your admin for a platform agent");
        if (builder) composerExpertPicks.queue(roomId, { id: builder.id });
        else toast.info("No Expert builder is installed: ask your admin to publish the default content, or describe what you want to your agent");
        const draft = workshopDraft(builder, req);
        if (draft) composerDrafts.queue(roomId, draft);
        onOpenRoom(roomId);
      } catch (e) {
        toast.error(errorText(e, "Could not open the workshop"));
      } finally {
        setBusy(false);
      }
    },
    [subject, reauth, busy, toast, onOpenRoom],
  );
  return { open, busy };
}

// openInLibrary shows the Library tab of a component's kind.
export function openInLibrary(c: Pick<ContentComponent, "kind">): void {
  const tab = libraryTabOf(c.kind);
  if (tab) queueLibraryTab(tab);
  window.location.hash = routeHash({ kind: "view", view: "library" });
}

// tryInRoom puts a saved component to use in a room: an expert is picked in
// the composer (with its first quick prompt); a skill pack or a prompt pack
// is turned on for the room (its owner or admins may), so the next turn
// loads the skills, or the composer offers the prompts (a pack's first
// slash command is put in the composer).
export async function tryInRoom(roomId: string, c: ContentComponent): Promise<string | null> {
  if (c.kind === "expert") {
    composerExpertPicks.queue(roomId, { id: c.id });
    const draft = tryItDraft(c);
    if (draft) composerDrafts.queue(roomId, draft);
    return null;
  }
  try {
    await contentApi.putUse("room", c.id, { roomId });
    if (c.kind === "prompt") {
      announcePromptPacksChanged();
      const draft = tryItDraft(c);
      if (draft) composerDrafts.queue(roomId, draft);
    } else composerDrafts.queue(roomId, `Use the ${c.name} skill to `);
    return null;
  } catch {
    return `Turn ${contentLabel(c)} on in the Library to try it`;
  }
}

// savedActions are a save toast's actions: "Open in Library" and, in a
// room, "Try it now".
export function savedActions(c: ContentComponent, roomId: string | null, onTryFailed: (msg: string) => void): ToastAction[] {
  const out: ToastAction[] = [{ label: "Open in Library", onClick: () => openInLibrary(c) }];
  if (roomId)
    out.push({
      label: "Try it now",
      onClick: () => {
        void tryInRoom(roomId, c).then((msg) => msg && onTryFailed(msg));
      },
    });
  return out;
}

export function savedMessage(c: ContentComponent): string {
  return `Saved ${contentLabel(c)} v${c.latestVersion}`;
}
