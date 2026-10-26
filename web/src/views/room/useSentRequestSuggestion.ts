import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { api } from "@/lib/api";
import { libraryApi } from "@/lib/libraryApi";
import { gatewayBase } from "@/lib/config";
import { contentApi } from "@/lib/contentComponentsApi";
import { decisionsApi } from "@/lib/decisions";
import { ApiError, errorText } from "@/lib/errors";
import { sentRequestCards, staleAnswer } from "@/lib/sentRequestCard";
import { StorageKey, browserStorage, memberKey } from "@/lib/storageKeys";
import {
  aiSuggestionsOn,
  asksAbout,
  candidatesOf,
  DECISION_BACKOFF_MS,
  decisionRequest,
  dismissSuggestion,
  dismissedKeys,
  eligibleForAsk,
  suggestionFromDecision,
  type SentRequest,
  type UseCandidate,
} from "@/lib/suggestUse";
import { useToast } from "@/ui/Toast";
import type { RoomAgent } from "@/views/composer/types";
import type { UseSuggestionActions } from "@/views/room/UseSuggestionCard";
import type { SendOptions } from "@/views/room/useRoomMessageActions";

/** NO_DECISION_MS: how long "the organization has no decision model" is believed. */
const NO_DECISION_MS = 5 * 60_000;

// useSentRequestSuggestion says what fits a request the member sent
// (docs/prd/2026-10-25-suggest-what-to-use.md): once the server took a message that
// went to an agent, it asks the organization's decision model one question about that
// message, and shows the answer as a card under it (views/room/UseSuggestionCard.tsx)
// while the agent works. Nothing is delayed or changed by it: the member's agent has
// already started. The card's button does the one thing: it turns the candidate on and
// sends the same request again through the room's normal send (so the expert's
// confirmation, the borrow check and the rest apply as for any send). Nothing happens
// without that click.
export function useSentRequestSuggestion({
  roomId,
  subject,
  roomAgents,
  send,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly roomAgents: readonly RoomAgent[];
  readonly send: (text: string, opts: SendOptions) => Promise<boolean>;
}): {
  /** Called with every message the room accepted from the member. */
  readonly observe: (sent: SentRequest & { readonly clientMsgId: string; readonly recipient: RoomAgent | null }) => void;
  readonly actions: UseSuggestionActions;
} {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const latest = useRef<string | null>(null);
  const left = useRef(false);
  const noDecisionUntil = useRef(0);
  const blockedUntil = useRef(0);
  const roomRef = useRef(roomId);
  roomRef.current = roomId;
  const card = useSyncExternalStore(sentRequestCards.subscribe, () => sentRequestCards.of(roomId));

  // A card never outlives its room, and an answer that lands after the member left is dropped.
  useEffect(() => {
    left.current = false;
    latest.current = null;
    return () => {
      left.current = true;
      sentRequestCards.clear(roomId);
    };
  }, [roomId]);

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

  const observe = useCallback(
    (sent: SentRequest & { readonly clientMsgId: string; readonly recipient: RoomAgent | null }) => {
      // A newer message makes an older card stale; a message nobody asks about shows none.
      latest.current = sent.clientMsgId;
      sentRequestCards.clear(roomRef.current);
      if (!keys || !asksAbout(sent) || !aiSuggestionsOn(browserStorage(), keys.ai)) return;
      const now = Date.now();
      if (now < blockedUntil.current || now < noDecisionUntil.current) return;
      const askedRoom = roomRef.current;
      void (async () => {
        try {
          // The server finds the candidates (a bounded shortlist, whatever the size of the Library);
          // the room's uses say what is already on.
          const [shortlist, uses] = await Promise.all([libraryApi.candidates(sent.text), contentApi.uses(askedRoom)]);
          const candidates = candidatesOf(shortlist);
          const exclude = dismissedKeys(browserStorage(), keys.dismissed, Date.now());
          for (const u of [...(uses.member ?? []), ...(uses.room ?? [])]) exclude.add(`${u.kind}/${u.componentId}`);
          if (uses.roomExpert) exclude.add(`expert/${uses.roomExpert}`);
          const worn = new Set(candidates.filter((c) => c.kind === "expert" && exclude.has(c.key)).flatMap((c) => c.wears));
          const eligible = eligibleForAsk(candidates, { exclude, worn });
          if (eligible.length === 0) return;
          const reply = await decisionsApi.decide(decisionRequest(sent.text, eligible));
          const found = suggestionFromDecision(reply, eligible);
          if (!found || staleAnswer(sent.clientMsgId, latest.current, left.current || roomRef.current !== askedRoom)) return;
          sentRequestCards.show(askedRoom, { clientMsgId: sent.clientMsgId, request: sent.text, suggestion: found });
        } catch (e) {
          // No decision model (409), a rate limit or a failure: nothing is shown, and the
          // model is left alone for a while.
          if (e instanceof ApiError && e.status === 409) noDecisionUntil.current = Date.now() + NO_DECISION_MS;
          else blockedUntil.current = Date.now() + DECISION_BACKOFF_MS;
        }
      })();
    },
    [keys],
  );

  const dismiss = useCallback(() => {
    const c = sentRequestCards.of(roomRef.current);
    if (c && keys) dismissSuggestion(browserStorage(), keys.dismissed, c.suggestion.candidate.key, Date.now());
    sentRequestCards.clear(roomRef.current);
  }, [keys]);

  const accept = useCallback(() => {
    const c = sentRequestCards.of(roomRef.current);
    if (!c || busy) return;
    const cand = c.suggestion.candidate;
    setBusy(true);
    void (async () => {
      try {
        await turnOn(cand, roomRef.current);
        if (keys) dismissSuggestion(browserStorage(), keys.dismissed, cand.key, Date.now());
        sentRequestCards.clear(roomRef.current);
        // The same request again, as a normal send: to the agent it went to, wearing the
        // expert when the candidate is one.
        const first = roomAgents.find((a) => a.self && !a.platform) ?? roomAgents[0] ?? null;
        const opts: SendOptions = { peopleOnly: false, recipient: first, ...(cand.kind === "expert" ? expertOf(cand) : {}) };
        const sent = await send(c.request, opts);
        if (!sent) toast.error("Could not send the request again");
        else if (cand.kind === "skill") toast.success(`${cand.label} is on; the request was sent again.`);
        else if (cand.kind === "mcp") toast.success(`${cand.label} added; the request was sent again.`);
      } catch (e) {
        toast.error(errorText(e, `Could not use ${cand.label}`));
      } finally {
        setBusy(false);
      }
    })();
  }, [busy, keys, roomAgents, send, toast]);

  return { observe, actions: { room: roomId, accept, dismiss, busy: busy || card === null } };
}

// turnOn makes the candidate available: a skill pack for the room (the owner or an admin;
// else the member's own agents), a tool added. An expert needs nothing: it is picked for
// the send that follows.
async function turnOn(c: UseCandidate, roomId: string): Promise<void> {
  if (c.kind === "skill") {
    try {
      await contentApi.putUse("room", c.id, { roomId, version: c.version });
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 403) throw e;
      await contentApi.putUse("member", c.id, { version: c.version });
    }
  } else if (c.kind === "mcp") {
    await api.installComponent({ kind: "mcp", name: c.name });
  }
}

// expertOf is the send's expert field and the optimistic row's marker for an expert
// candidate (as useComposerExpert.sendField builds them).
function expertOf(c: UseCandidate): Pick<SendOptions, "expert" | "expertMarker"> {
  return {
    expert: { id: c.id },
    expertMarker: { id: c.id, name: c.name, displayName: c.label, version: c.version ?? "" },
  };
}
