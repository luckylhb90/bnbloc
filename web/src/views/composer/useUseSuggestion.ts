import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/lib/api";
import { gatewayBase } from "@/lib/config";
import type { ContentUsesReply } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { contentUsesChanged } from "@/lib/contentUsesEvents";
import { decisionsApi } from "@/lib/decisions";
import { ApiError, errorText } from "@/lib/errors";
import { StorageKey, browserStorage, memberKey } from "@/lib/storageKeys";
import {
  aiSuggestionsOn,
  asked,
  candidatesOf,
  DECISION_BACKOFF_MS,
  decisionRequest,
  dismissSuggestion,
  dismissedKeys,
  eligibleForAsk,
  MIN_DRAFT_CHARS,
  NO_ASKS,
  shouldAsk,
  suggestionFromDecision,
  suggestUse,
  type AskState,
  type UseCandidate,
  type UseSuggestion,
} from "@/lib/suggestUse";
import { useContentChanged } from "@/lib/useContentChanged";
import { useToast } from "@/ui/Toast";
import type { ExpertChoice } from "@/lib/contentComponents";

/** WARM_CHARS: the Library is read as soon as a request has this many characters, so it
 *  is there by the time the member pauses. */
const WARM_CHARS = 5;
/** SETTLE_MS: how long the member pauses before a request is placed. */
const SETTLE_MS = 700;
/** CATALOG_FRESH_MS: how long the Library read for suggestions is reused. */
const CATALOG_FRESH_MS = 5 * 60_000;
/** NO_DECISION_MS: how long "the organization has no decision model" is believed. */
const NO_DECISION_MS = 5 * 60_000;

export interface UseSuggestionState {
  readonly suggestion: UseSuggestion | null;
  readonly busy: boolean;
  readonly accept: () => void;
  readonly dismiss: () => void;
}

// useUseSuggestion recommends one expert, skill pack or tool for what the member
// is writing (docs/prd/2026-10-25-suggest-what-to-use.md). The organization's
// decision model places the request when it runs for the member and they have not
// turned it off (a few calls per draft, never per keystroke: lib/suggestUse's
// ask rules); otherwise lib/suggestUse's keyword matcher does, locally. The hook
// reads the Library when a request is first worth placing, leaves out what is
// already on, picked or dismissed, and applies the member's click with the
// Library's own calls. Nothing happens without it.
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
  const warm = enabled && draft.trim().length >= WARM_CHARS;
  useEffect(() => {
    if (!warm || (loadedAt.current && Date.now() - loadedAt.current < CATALOG_FRESH_MS)) return;
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
  }, [warm, roomId, epoch]);

  const keys = useMemo(
    () =>
      subject
        ? {
            dismissed: memberKey(StorageKey.useSuggestions, { gateway: gatewayBase(), subject }),
            ai: memberKey(StorageKey.suggestWithAi, { gateway: gatewayBase(), subject }),
          }
        : null,
    [subject],
  );

  // What is not offered: dismissed, already on, picked or the room's expert, an
  // expert that cannot be picked here; and the skill packs an expert in effect or
  // dismissed wears (they are not offered in its place).
  const eligibility = useMemo(() => {
    if (!candidates || !uses || !keys) return null;
    const exclude = dismissedKeys(browserStorage(), keys.dismissed, Date.now());
    for (const u of [...(uses.member ?? []), ...(uses.room ?? [])]) exclude.add(`${u.kind}/${u.componentId}`);
    if (picked) exclude.add(`expert/${picked.component.id}`);
    if (uses.roomExpert) exclude.add(`expert/${uses.roomExpert}`);
    const pickable = new Set(choices.map((c) => c.component.id));
    for (const c of candidates) if (c.kind === "expert" && !pickable.has(c.id)) exclude.add(c.key);
    const worn = new Set(candidates.filter((c) => c.kind === "expert" && exclude.has(c.key)).flatMap((c) => c.wears));
    return { exclude, worn };
    // dismissedAt re-reads the dismissals after one.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [candidates, uses, keys, picked, choices, dismissedAt]);

  // The decision model's side: asked after a pause, a few times per draft, never
  // while one is running for older text, and out of it for a minute after a failure.
  const [ai, setAi] = useState<UseSuggestion | null>(null);
  const [aiBlockedUntil, setAiBlockedUntil] = useState(0);
  const askState = useRef<AskState>(NO_ASKS);
  const seq = useRef(0);
  const inflight = useRef<AbortController | null>(null);
  const noDecisionUntil = useRef(0);
  const availableAt = useRef(0);
  const aiOn = !!keys && aiSuggestionsOn(browserStorage(), keys.ai);
  const aiUsable = aiOn && Date.now() >= aiBlockedUntil;

  // A new draft starts over; the member clearing the box drops what was asked.
  useEffect(() => {
    if (draft.trim() !== "") return;
    askState.current = NO_ASKS;
    seq.current++;
    inflight.current?.abort();
    setAi(null);
  }, [draft]);
  useEffect(() => () => inflight.current?.abort(), []);

  useEffect(() => {
    if (!placing || !eligibility || !candidates || !aiUsable) return;
    const now = Date.now();
    if (!shouldAsk(askState.current, settled, now)) return;
    const eligible = eligibleForAsk(settled, candidates, eligibility);
    if (eligible.length === 0) return;
    askState.current = asked(askState.current, settled, now);
    const mine = ++seq.current;
    inflight.current?.abort();
    const ctl = new AbortController();
    inflight.current = ctl;
    void (async () => {
      try {
        if (now < noDecisionUntil.current) throw new Error("no decision model");
        if (now - availableAt.current > NO_DECISION_MS) {
          const a = await decisionsApi.available();
          availableAt.current = Date.now();
          if (!a.available) {
            noDecisionUntil.current = Date.now() + NO_DECISION_MS;
            throw new Error("no decision model");
          }
        }
        const reply = await decisionsApi.decide(decisionRequest(settled, eligible), ctl.signal);
        if (mine === seq.current) setAi(suggestionFromDecision(reply, eligible));
      } catch (e) {
        if (ctl.signal.aborted || mine !== seq.current) return;
        // Off, unavailable, throttled or failed: the keyword matcher takes over for a while.
        if (!(e instanceof ApiError) || e.status === 409) noDecisionUntil.current = Date.now() + NO_DECISION_MS;
        setAiBlockedUntil(Date.now() + DECISION_BACKOFF_MS);
        setAi(null);
      }
    })();
  }, [placing, settled, eligibility, candidates, aiUsable]);

  const suggestion = useMemo(() => {
    if (!placing || !eligibility || !candidates) return null;
    // The decision model, when it runs for the member; its answer stands (silence included).
    if (aiUsable) return ai && !eligibility.exclude.has(ai.candidate.key) ? ai : null;
    return suggestUse(settled, candidates, eligibility);
  }, [placing, settled, candidates, eligibility, ai, aiUsable]);

  const dismiss = useCallback(() => {
    if (!suggestion || !keys) return;
    dismissSuggestion(browserStorage(), keys.dismissed, suggestion.candidate.key, Date.now());
    setDismissedAt((n) => n + 1);
  }, [suggestion, keys]);

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
        if (keys) dismissSuggestion(browserStorage(), keys.dismissed, c.key, Date.now());
        setDismissedAt((n) => n + 1);
        setAi(null);
        reload();
      } catch (e) {
        toast.error(errorText(e, `Could not turn on ${c.label}`));
      } finally {
        setBusy(false);
      }
    })();
  }, [suggestion, busy, choices, pickExpert, roomId, keys, toast, reload]);

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
