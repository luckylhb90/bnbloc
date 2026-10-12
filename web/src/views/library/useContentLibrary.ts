import { useCallback, useEffect, useState } from "react";
import { contentApi } from "@/lib/contentComponentsApi";
import { contentKindWord, contentLabel, type ContentComponent, type ContentKind } from "@/lib/contentComponents";
import { errorText } from "@/lib/errors";
import { useToast } from "@/ui/Toast";

// useContentLibrary is one Library tab's data (web M30): the member's
// components of a kind, which of them are on for the member (skill and
// prompt packs), and the actions every tab offers. onChanged runs after
// anything the member turned on, off or deleted (the prompt tab tells the
// composer). A failed load is an error, never an empty list (web M20).
export function useContentLibrary(
  kind: ContentKind,
  opts: { readonly onText: (c: ContentComponent, on: boolean) => string; readonly onChanged?: () => void },
) {
  const toast = useToast();
  const { onText, onChanged } = opts;
  const [items, setItems] = useState<ContentComponent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [enabled, setEnabled] = useState<ReadonlySet<string>>(new Set());
  const [epoch, setEpoch] = useState(0);
  const reload = useCallback(() => setEpoch((n) => n + 1), []);
  const memberUse = kind !== "expert";

  useEffect(() => {
    let live = true;
    setItems(null);
    setError(null);
    Promise.all([contentApi.list(kind), memberUse ? contentApi.uses() : Promise.resolve({ member: [] })])
      .then(([list, uses]) => {
        if (!live) return;
        setItems(list.components ?? []);
        setEnabled(new Set(uses.member.map((u) => u.componentId)));
      })
      .catch((e) => live && setError(errorText(e, `Could not load your ${contentKindWord(kind)}s`)));
    return () => {
      live = false;
    };
  }, [kind, memberUse, epoch]);

  const remove = useCallback(
    async (c: ContentComponent) => {
      try {
        await contentApi.remove(c.id);
        toast.success(`Deleted ${c.name}`);
        onChanged?.();
        reload();
      } catch (e) {
        toast.error(errorText(e, "Could not delete it"));
      }
    },
    [toast, onChanged, reload],
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
        onChanged?.();
        toast.success(onText(c, on));
      } catch (e) {
        toast.error(errorText(e, `Could not turn ${contentLabel(c)} ${on ? "on" : "off"}`));
      }
    },
    [toast, onText, onChanged],
  );

  return { items, error, enabled, reload, remove, toggleForMe };
}
