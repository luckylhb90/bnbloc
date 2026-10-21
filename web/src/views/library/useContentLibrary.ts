import { useCallback, useEffect, useRef, useState } from "react";
import { contentApi } from "@/lib/contentComponentsApi";
import { contentKindWord, contentLabel, type ContentComponent, type ContentKind } from "@/lib/contentComponents";
import { errorText } from "@/lib/errors";
import { useContentChanged } from "@/lib/useContentChanged";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { useToast } from "@/ui/Toast";

// A change made outside this page (another tab, another member, an admin
// removing organization content, the API) reaches an open tab this often,
// and at once when the page becomes visible again.
const REREAD_MS = 30_000;

// useContentLibrary is one Library tab's data (web M30): the member's
// components of a kind, which of them are on for the member (skill and
// prompt packs), and the actions every tab offers. The list reads again
// after every content change of its kind (lib/contentChanges.ts: every
// mutation announces itself, so no action here or in a dialog reloads by
// hand) and on the visible re-read; a re-read keeps the shown list until
// its answer is in, and a failed re-read keeps it (the next one tries
// again). A failed first load is an error, never an empty list (web M20).
export function useContentLibrary(
  kind: ContentKind,
  opts: { readonly onText: (c: ContentComponent, on: boolean) => string },
) {
  const toast = useToast();
  const { onText } = opts;
  const [items, setItems] = useState<ContentComponent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [enabled, setEnabled] = useState<ReadonlySet<string>>(new Set());
  const memberUse = kind !== "expert";
  // The latest read wins: an older answer landing late never replaces it.
  const readSeq = useRef(0);
  const loaded = useRef(false);

  const read = useCallback(() => {
    const seq = ++readSeq.current;
    Promise.all([contentApi.list(kind), memberUse ? contentApi.uses() : Promise.resolve({ member: [] })])
      .then(([list, uses]) => {
        if (seq !== readSeq.current) return;
        loaded.current = true;
        setItems(list.components ?? []);
        setEnabled(new Set(uses.member.map((u) => u.componentId)));
        setError(null);
      })
      .catch((e) => {
        if (seq !== readSeq.current || loaded.current) return;
        setError(errorText(e, `Could not load your ${contentKindWord(kind)}s`));
      });
  }, [kind, memberUse]);

  useEffect(() => {
    loaded.current = false;
    setItems(null);
    setError(null);
    read();
    return () => {
      readSeq.current++;
    };
  }, [read]);
  useContentChanged([kind], read);
  useVisiblePoll(read, REREAD_MS);
  // Opening the Library again (a link or a search into it while it is
  // shown: the hash changes, the view stays mounted) reads it again.
  useEffect(() => {
    window.addEventListener("hashchange", read);
    return () => window.removeEventListener("hashchange", read);
  }, [read]);

  // Retry after a failed load starts over from the skeleton.
  const reload = useCallback(() => {
    loaded.current = false;
    setItems(null);
    setError(null);
    read();
  }, [read]);

  const remove = useCallback(
    async (c: ContentComponent) => {
      try {
        await contentApi.remove(c.id);
        toast.success(`Deleted ${c.name}`);
      } catch (e) {
        toast.error(errorText(e, "Could not delete it"));
      }
    },
    [toast],
  );

  const toggleForMe = useCallback(
    async (c: ContentComponent, on: boolean) => {
      try {
        if (on) await contentApi.putUse("member", c.id);
        else await contentApi.removeUse("member", c.id);
        setEnabled((cur) => {
          const next = new Set(cur);
          if (on) next.add(c.id);
          else next.delete(c.id);
          return next;
        });
        toast.success(onText(c, on));
      } catch (e) {
        toast.error(errorText(e, `Could not turn ${contentLabel(c)} ${on ? "on" : "off"}`));
      }
    },
    [toast, onText],
  );

  return { items, error, enabled, reload, remove, toggleForMe };
}
