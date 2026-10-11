import { useEffect, type Dispatch, type RefObject, type SetStateAction } from "react";
import { composerAgentPicks, composerDrafts, composerRefs, mergeDraft } from "@/lib/team";
import { samePlatformAgent } from "@/lib/platformAgents";
import type { Referenceable, RoomAgent } from "@/views/composer/types";
import { nextFeedId } from "@/views/room/types";

// useInjectedDraft lets another view hand the room's composer a draft (the
// prompt library's "Use"): a draft queued before the room opened is applied
// once the composer has its channel, and one queued while it is open applies
// at once. It is merged into whatever the member was typing, never replacing
// it, and the caret lands at the end. The draft is taken exactly once.
export function useInjectedDraft(
  roomId: string,
  channelId: string | null | undefined,
  setText: Dispatch<SetStateAction<string>>,
  textareaRef: RefObject<HTMLTextAreaElement | null>,
): void {
  useEffect(() => {
    // The composer remounts per channel; wait for the real one so the draft is
    // not consumed by the placeholder mounted before the channel resolves.
    if (!channelId) return;
    const apply = () => {
      const draft = composerDrafts.take(roomId);
      if (draft == null) return;
      setText((cur) => mergeDraft(cur, draft));
      window.requestAnimationFrame(() => {
        const el = textareaRef.current;
        if (!el) return;
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      });
    };
    apply();
    return composerDrafts.subscribe((id) => {
      if (id === roomId) apply();
    });
  }, [roomId, channelId, setText, textareaRef]);
}

// useInjectedReferences hands the room's main composer quoted references
// from elsewhere (the people chat's "Ask the agent about this", possibly from
// a popped-out window): queued refs are appended as reference chips, never
// sent — the member reviews and sends them. Each ref gets a fresh local id so
// it cannot collide with a chip picked from this feed.
export function useInjectedReferences(
  roomId: string,
  channelId: string | null | undefined,
  enabled: boolean,
  setRefs: Dispatch<SetStateAction<Referenceable[]>>,
  textareaRef: RefObject<HTMLTextAreaElement | null>,
): void {
  useEffect(() => {
    if (!channelId || !enabled) return;
    const apply = () => {
      const refs = composerRefs.take(roomId);
      if (!refs || refs.length === 0) return;
      setRefs((cur) => [...cur, ...refs.map((r) => ({ ...r, id: nextFeedId() }))]);
      window.requestAnimationFrame(() => textareaRef.current?.focus());
    };
    apply();
    return composerRefs.subscribe((id) => {
      if (id === roomId) apply();
    });
  }, [roomId, channelId, enabled, setRefs, textareaRef]);
}

// useInjectedAgent selects the agent another view asked for (the welcome
// card opens its new room with a platform agent, or the member's own agent,
// picked). The pick waits
// until the room's agents list it, then is taken exactly once.
export function useInjectedAgent(
  roomId: string,
  channelId: string | null | undefined,
  roomAgents: readonly RoomAgent[],
  pick: (agent: RoomAgent) => void,
): void {
  useEffect(() => {
    if (!channelId) return;
    const apply = () => {
      const want = composerAgentPicks.peek(roomId);
      if (!want) return;
      const match = roomAgents.find(
        (a) =>
          (want.own ? a.self && !a.platform : !!a.platform) &&
          samePlatformAgent({ core: a.core ?? "", provider: a.provider }, want),
      );
      if (!match) return;
      composerAgentPicks.take(roomId);
      pick(match);
    };
    apply();
    return composerAgentPicks.subscribe((id) => {
      if (id === roomId) apply();
    });
  }, [roomId, channelId, roomAgents, pick]);
}
