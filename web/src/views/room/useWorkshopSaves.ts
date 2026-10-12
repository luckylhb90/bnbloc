import { useEffect, useMemo, useRef, useState } from "react";
import { BUILDER_EXPERT_ID } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { browserStorage } from "@/lib/platformRouting";
import { cachedWorkshop, librarySaves, librarySnapshot, type LibrarySnapshot } from "@/lib/workshop";
import { useToast } from "@/ui/Toast";
import { savedActions, savedMessage } from "@/views/library/workshopFlow";
import type { FeedItem } from "@/views/room/types";

// After an agent reply settles, the library is re-read this much later: a
// save through the room tools lands before the reply that reports it, but
// give the gateway write a moment.
const SETTLE_MS = 1500;

// useWorkshopSaves announces what the builder saved while the member works
// in a workshop room (the cached workshop, or any room pinning the builder):
// on each new agent reply it re-lists the member's library and diffs it
// against the snapshot taken when the room opened, then toasts each new
// component or version with "Open in Library" and "Try it now".
export function useWorkshopSaves(roomId: string, subject: string | null, feed: readonly FeedItem[]): void {
  const toast = useToast();
  const [active, setActive] = useState(() => cachedWorkshop(browserStorage(), subject) === roomId);
  useEffect(() => {
    if (active) return;
    let live = true;
    contentApi
      .uses(roomId)
      .then((u) => live && (u.room ?? []).some((x) => x.componentId === BUILDER_EXPERT_ID) && setActive(true))
      .catch(() => {}); // not a workshop as far as we can tell
    return () => {
      live = false;
    };
  }, [roomId, active]);

  const snapshot = useRef<LibrarySnapshot | null>(null);
  useEffect(() => {
    if (!active) return;
    let live = true;
    contentApi
      .list()
      .then((r) => {
        if (live) snapshot.current = librarySnapshot(r.components ?? []);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [active]);

  const replies = useMemo(() => feed.filter((m) => m.agentLabel && m.seq != null).length, [feed]);
  const seen = useRef(replies);
  useEffect(() => {
    if (!active || replies === seen.current) return;
    seen.current = replies;
    let live = true;
    const timer = window.setTimeout(() => {
      contentApi
        .list()
        .then((r) => {
          const before = snapshot.current;
          const list = r.components ?? [];
          snapshot.current = librarySnapshot(list);
          if (!live || !before) return;
          for (const c of librarySaves(before, list)) toast.success(savedMessage(c), savedActions(c, roomId, (msg) => toast.info(msg)));
        })
        .catch(() => {}); // best-effort: the Library shows the save anyway
    }, SETTLE_MS);
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [active, replies, roomId, toast]);
}
