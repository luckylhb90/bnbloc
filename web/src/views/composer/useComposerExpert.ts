import { useCallback, useEffect, useMemo, useState } from "react";
import {
  chatExpert,
  expertChoices,
  expertMentionMatches,
  needsConfirmation,
  type ChatExpert,
  type ExpertChoice,
  type ExpertMarker,
} from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";

export interface ComposerExpert {
  // The member's own experts and the room's pinned ones (room pins first).
  readonly choices: readonly ExpertChoice[];
  // The picked expert (sticky across sends, like the agent), or null.
  readonly expert: ExpertChoice | null;
  readonly pick: (e: ExpertChoice | null) => void;
  // @expert candidates for a typed mention query.
  readonly mentionMatches: (query: string) => ExpertChoice[];
  // The expert awaiting the member's first-use confirmation of its
  // declared permissions (the dialog's subject), and its answers.
  readonly confirming: ExpertChoice | null;
  readonly resolveConfirm: (ok: boolean) => void;
  // confirm resolves true once the picked expert may be sent: it is the
  // member's own, declares nothing, or they confirmed it (recorded).
  readonly confirm: () => Promise<boolean>;
  // The send's expert field and the optimistic row's marker.
  readonly sendField: () => { expert: ChatExpert; marker: ExpertMarker } | undefined;
  readonly reload: () => void;
}

// useComposerExpert loads the experts a room's composer offers and owns the
// picked one. Disabled (the people chat) it offers none.
export function useComposerExpert(roomId: string, enabled: boolean): ComposerExpert {
  const [choices, setChoices] = useState<readonly ExpertChoice[]>([]);
  const [expert, setExpert] = useState<ExpertChoice | null>(null);
  const [confirming, setConfirming] = useState<{ choice: ExpertChoice; resolve: (ok: boolean) => void } | null>(null);
  const [epoch, setEpoch] = useState(0);

  useEffect(() => {
    if (!enabled) return;
    let live = true;
    Promise.all([contentApi.list("expert"), contentApi.uses(roomId)])
      .then(([mine, uses]) => live && setChoices(expertChoices(mine.components ?? [], uses.room ?? [])))
      .catch(() => live && setChoices([])); // experts are optional: the composer works without them
    return () => {
      live = false;
    };
  }, [roomId, enabled, epoch]);

  // A picked expert that left the choices (deleted, unpinned) is dropped.
  useEffect(() => {
    if (expert && !choices.some((c) => c.component.id === expert.component.id)) setExpert(null);
  }, [choices, expert]);

  const mentionMatches = useCallback((query: string) => (enabled ? expertMentionMatches(choices, query) : []), [choices, enabled]);

  const confirm = useCallback(async (): Promise<boolean> => {
    if (!expert || !needsConfirmation(expert.component)) return true;
    const ok = await new Promise<boolean>((resolve) => setConfirming({ choice: expert, resolve }));
    setConfirming(null);
    if (!ok) return false;
    const c = expert.component;
    const version = expert.version || c.version || c.latestVersion;
    try {
      await contentApi.ack(c.id, version, expert.source === "room" ? roomId : undefined);
    } catch {
      // The confirmation is recorded best-effort; the member said yes.
    }
    const acked: ExpertChoice = { ...expert, component: { ...c, acked: true } };
    setExpert(acked);
    setChoices((cur) => cur.map((x) => (x.component.id === c.id ? acked : x)));
    return true;
  }, [expert, roomId]);

  const sendField = useCallback(() => {
    if (!expert) return undefined;
    const c = expert.component;
    return {
      expert: chatExpert(expert),
      marker: { id: c.id, name: c.name, displayName: c.displayName, version: expert.version || c.latestVersion },
    };
  }, [expert]);

  return useMemo(
    () => ({
      choices,
      expert,
      pick: setExpert,
      mentionMatches,
      confirming: confirming?.choice ?? null,
      resolveConfirm: (ok: boolean) => confirming?.resolve(ok),
      confirm,
      sendField,
      reload: () => setEpoch((n) => n + 1),
    }),
    [choices, expert, mentionMatches, confirming, confirm, sendField],
  );
}
