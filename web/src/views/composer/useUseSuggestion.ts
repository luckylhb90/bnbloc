import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/lib/api";
import { gatewayBase } from "@/lib/config";
import type { ContentUsesReply } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { contentUsesChanged } from "@/lib/contentUsesEvents";
import { ApiError, errorText } from "@/lib/errors";
import { StorageKey, browserStorage, memberKey } from "@/lib/storageKeys";
import {
  candidatesOf,
  dismissSuggestion,
  dismissedKeys,
  MIN_DRAFT_CHARS,
  suggestUse,
  type UseCandidate,
  type UseSuggestion,
} from "@/lib/suggestUse";
import { useContentChanged } from "@/lib/useContentChanged";
import { useToast } from "@/ui/Toast";
import type { ExpertChoice } from "@/lib/contentComponents";

/** SETTLE_MS: how long the member pauses before a request is placed. */
const SETTLE_MS = 700;
/** CATALOG_FRESH_MS: how long the Library read for suggestions is reused. */
const CATALOG_FRESH_MS = 5 * 60_000;

export interface UseSuggestionState {
  readonly suggestion: UseSuggestion | null;
  readonly busy: boolean;
  readonly accept: () => void;
  readonly dismiss: () => void;
}

// useUseSuggestion recommends one expert, skill pack or tool for what the member
// is writing (docs/prd/2026-10-25-suggest-what-to-use.md): the ranking is
// lib/suggestUse's, local; this hook reads the Library when a request is first
// worth placing, leaves out what is already on, picked or dismissed, and applies
// the member's click with the Library's own calls. Nothing happens without it.
export function useUseSuggestion({
  roomId,
  enabled,
  draft,
  subject,
  picked,
  choices,
  pickExpert,
}: {
  readonly roomId: string;
  /** False where there is nothing to suggest for (the people chat, a file panel). */
  readonly enabled: boolean;
  readonly draft: string;
  readonly subject: string | null;
  readonly picked: ExpertChoice | null;
  /** The experts the composer can pick. */
  readonly choices: readonly ExpertChoice[];
  readonly pickExpert: (e: ExpertChoice | null) => void;
}): UseSuggestionState {
  const toast = useToast();
  const [candidates, setCandidates] = useState<readonly UseCandidate[] | null>(null);
  const [uses, setUses] = useState<ContentUsesReply | null>(null);
  const [settled, setSettled] = useState("");
  const [epoch, setEpoch] = useState(0);
  const [dismissedAt, setDismissedAt] = useState(0);
  const [busy, setBusy] = useState(false);
  const loadedAt = useRef(0);

  // The request counts once the member pauses, and only when it says enough.
  const wanted = enabled && draft.trim().length >= MIN_DRAFT_CHARS;
  useEffect(() => {
    if (!wanted) {
      setSettled("");
      return;
    }
    const t = window.setTimeout(() => setSettled(draft), SETTLE_MS);
    return () => window.clearTimeout(t);
  }, [wanted, draft]);

  // The Library and what is on, read when a request first needs placing and again
  // when either changes (a skill turned on here or in another window, a tool added).
  const reload = useCallback(() => {
    loadedAt.current = 0;
    setEpoch((n) => n + 1);
  }, []);
  useContentChanged(["expert", "skill"], reload);
  useEffect(() => {
    if (!enabled) return;
    return contentUsesChanged.subscribe((id) => {
      if (id === roomId) reload();
    });
  }, [enabled, roomId, reload]);
  const placing = settled !== "";
  useEffect(() => {
    if (!placing || (loadedAt.current && Date.now() - loadedAt.current < CATALOG_FRESH_MS)) return;
    let live = true;
    Promise.all([api.components(), contentApi.uses(roomId)])
      .then(([catalog, u]) => {
        if (!live) return;
        loadedAt.current = Date.now();
        setCandidates(candidatesOf(catalog));
        setUses(u);
      })
      .catch(() => {}); // suggestions are optional: the composer works without them
    return () => {
      live = false;
    };
  }, [placing, roomId, epoch]);

  const storeKey = useMemo(
    () => (subject ? memberKey(StorageKey.useSuggestions, { gateway: gatewayBase(), subject }) : ""),
    [subject],
  );

  const suggestion = useMemo(() => {
    if (!placing || !candidates || !uses || !storeKey) return null;
    const exclude = dismissedKeys(browserStorage(), storeKey, Date.now());
    const on = new Set<string>();
    for (const u of [...(uses.member ?? []), ...(uses.room ?? [])]) on.add(`${u.kind}/${u.componentId}`);
    for (const k of on) exclude.add(k);
    if (picked) exclude.add(`expert/${picked.component.id}`);
    if (uses.roomExpert) exclude.add(`expert/${uses.roomExpert}`);
    // An expert that cannot be picked here is not offered.
    const pickable = new Set(choices.map((c) => c.component.id));
    for (const c of candidates) if (c.kind === "expert" && !pickable.has(c.id)) exclude.add(c.key);
    // An expert in effect, or one the member dismissed, speaks for the skill packs it
    // wears: they are not offered in its place.
    const worn = new Set(candidates.filter((c) => c.kind === "expert" && exclude.has(c.key)).flatMap((c) => c.wears));
    return suggestUse(settled, candidates, { exclude, worn });
    // dismissedAt re-reads the dismissals after one.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [placing, settled, candidates, uses, storeKey, picked, choices, dismissedAt]);

  const dismiss = useCallback(() => {
    if (!suggestion || !storeKey) return;
    dismissSuggestion(browserStorage(), storeKey, suggestion.candidate.key, Date.now());
    setDismissedAt((n) => n + 1);
  }, [suggestion, storeKey]);

  const accept = useCallback(() => {
    const c = suggestion?.candidate;
    if (!c || busy) return;
    setBusy(true);
    void (async () => {
      try {
        if (c.kind === "expert") {
          const choice = choices.find((e) => e.component.id === c.id);
          if (!choice) throw new Error(`${c.label} is not available here`);
          pickExpert(choice);
        } else if (c.kind === "skill") {
          toast.success(await turnOnSkill(c, roomId));
        } else {
          await api.installComponent({ kind: "mcp", name: c.name });
          toast.success(`${c.label} added. Ask again and the agent can use it.`);
        }
        // It is on now: it is not suggested again.
        if (storeKey) dismissSuggestion(browserStorage(), storeKey, c.key, Date.now());
        setDismissedAt((n) => n + 1);
        reload();
      } catch (e) {
        toast.error(errorText(e, `Could not turn on ${c.label}`));
      } finally {
        setBusy(false);
      }
    })();
  }, [suggestion, busy, choices, pickExpert, roomId, storeKey, toast, reload]);

  return { suggestion, busy, accept, dismiss };
}

// turnOnSkill turns a skill pack on for the room, as the room's dialog does (the
// owner or an admin), and for the member's own agents when the room refuses.
async function turnOnSkill(c: UseCandidate, roomId: string): Promise<string> {
  try {
    await contentApi.putUse("room", c.id, { roomId, version: c.version });
    return `${c.label} is on for this room from the next message.`;
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 403) throw e;
    await contentApi.putUse("member", c.id, { version: c.version });
    return `${c.label} is on for your own agents from the next message (you cannot change this room's).`;
  }
}
