import { useEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { api } from "@/lib/api";
import { restoreAgent, savedAgent } from "@/lib/agentPrefs";
import { defaultRoomRecipient, type PlatformChoice } from "@/lib/platformAgents";
import { agentIdentity } from "@/lib/roomAgents";
import { composerDefault } from "@/lib/channelRules";
import type { ChannelAgent } from "@/lib/wire.gen";
import type { LocalAgent } from "@/lib/useLocalAgent";
import type { RoomAgent } from "@/views/composer/types";

export interface AgentPrefs {
  // The member's agents as this room sees them: `target` is the own local
  // target picked HERE (restored from the room's prefs), falling back to
  // the app-wide selection, which this room never overwrites.
  readonly agent: LocalAgent;
  // A picked room agent (another member's to borrow, a platform agent);
  // null drives my own selected target.
  readonly recipient: RoomAgent | null;
  readonly setRecipient: Dispatch<SetStateAction<RoomAgent | null>>;
  // People-only: the message drives no agent.
  readonly peopleOnly: boolean;
  readonly setPeopleOnly: Dispatch<SetStateAction<boolean>>;
  // Per-send tuning ("" = the agent's default).
  readonly model: string;
  readonly setModel: Dispatch<SetStateAction<string>>;
  readonly reasoning: string;
  readonly setReasoning: Dispatch<SetStateAction<string>>;
}

// useAgentPrefs owns the composer's agent selection for one room+channel:
// it restores the member's saved agent from the server (the only per-room
// store, shared across devices; channel row, then the room-level row),
// matching it by owner, core and provider (lib/agentPrefs.ts), lands on the
// room's platform agent when the member has none of their own, and saves
// changes back (debounced; a pending save is flushed on unmount).
export function useAgentPrefs({
  roomId,
  channelId,
  enabled,
  agent: localAgent,
  roomAgents,
  subject,
  offered,
  channelDefault,
}: {
  readonly roomId: string;
  readonly channelId: string | null | undefined;
  // False for the people chat: it has no agent selection.
  readonly enabled: boolean;
  readonly agent: LocalAgent;
  readonly roomAgents: readonly RoomAgent[];
  readonly subject: string | null;
  // The platform agents the organization offers (in the room or not): the
  // default of a member with no agent of their own when the room has none.
  readonly offered?: readonly PlatformChoice[];
  // The channel's default agent (its settings, C8), by its key.
  readonly channelDefault?: ChannelAgent | null;
}): AgentPrefs {
  const [recipient, setRecipient] = useState<RoomAgent | null>(null);
  const [peopleOnly, setPeopleOnly] = useState(false);
  const [model, setModel] = useState("");
  const [reasoning, setReasoning] = useState("");
  const [ownTarget, setOwnTarget] = useState("");
  const { targets } = localAgent;
  const target = targets.some((t) => t.id === ownTarget) ? ownTarget : localAgent.target;
  const agent = useMemo<LocalAgent>(
    () => ({ ...localAgent, target, core: targets.find((t) => t.id === target)?.core || "", setTarget: setOwnTarget }),
    [localAgent, targets, target],
  );

  const channelKey = channelDefault ? agentIdentity({ ownerId: channelDefault.ownerId, core: channelDefault.core || undefined, provider: channelDefault.provider }) : "";
  // The identity of the channel's default agent while it is the pick: a
  // default is not the member's choice, so it is not saved as theirs (the
  // channel's owners may change it later).
  const [channelDefaulted, setChannelDefaulted] = useState("");

  // Restore once per channel visit. It re-runs while the roster populates
  // (nothing to match against yet), stops once restored, and never clobbers
  // a selection the user made. Best-effort: a failed read leaves defaults.
  const restored = useRef(false);
  // The channel the restore read for: the default channel resolving while the
  // composer stays mounted (it keeps the draft) reads its stored pick again.
  const restoredFor = useRef(channelId);
  // The recipient was chosen as a default (not by the member).
  const defaulted = useRef(false);
  const [settled, setSettled] = useState(false);
  useEffect(() => {
    if (restored.current && restoredFor.current !== channelId) restored.current = false;
    if (!enabled || restored.current || roomAgents.length === 0) return;
    let cancelled = false;
    void (async () => {
      try {
        const ch = channelId ?? "";
        let p = await api.getAgentPrefs(roomId, ch);
        // C8: no choice of the member's for this channel: the channel's
        // default agent (when the room lists it for them) comes before
        // their room-level pick, which is the room's default.
        const channelFirst = composerDefault({ channelDefault, drivable: roomAgents, roomDefault: () => null }).from === "channel";
        if (!p.found && ch && !channelFirst) p = await api.getAgentPrefs(roomId, "");
        if (cancelled || restored.current) return;
        restored.current = true; // landed (or nothing stored): stop retrying
        restoredFor.current = channelId;
        setSettled(true);
        const r = restoreAgent(p, roomAgents, targets, subject);
        if (r && "ownTargetId" in r) {
          setOwnTarget(r.ownTargetId);
          setRecipient(null);
          setPeopleOnly(false);
        } else if (r) {
          // The stored pick replaces a default this hook chose before it was read.
          setRecipient((cur) => (cur && !defaulted.current ? cur : r.recipient));
          defaulted.current = false;
          setPeopleOnly(false);
        }
        // The stored tuning, unless the member already picked their own
        // while it was being read. It is never dropped for a catalog that
        // has not loaded yet (Composer's offeredChoice decides what is
        // sent), so a later save cannot write an empty pick over it.
        if (p.model) setModel((cur) => cur || p.model || "");
        if (p.reasoning) setReasoning((cur) => cur || p.reasoning || "");
      } catch {
        // no prefs / offline: defaults stand; a later roster change retries
        setSettled(true);
      }
    })();
    return () => {
      cancelled = true;
    };
    // channelDefault is channelKey's identity.
  }, [enabled, roomId, channelId, roomAgents, targets, subject, channelKey]);

  // A member with no agent of their own here (browser-only, nothing stored for
  // this room) lands on a default instead of "No local agent": their own
  // advertised agent, else a platform agent. It does not wait for the roster:
  // a new room's is empty for a few seconds, and a stored pick read later
  // replaces the default (restore, above).
  const rosterEmpty = roomAgents.length === 0;
  // decision: channels PRD C8. The member's own choice for the channel is
  // the stored pick (restored above, it replaces a default); else the
  // channel's default agent when it is one the room lists for the member
  // (own, borrowed with a grant, or a platform agent of the room), by its
  // key; else the room's default as before; else the picker asks. It is a
  // default for an empty pick only: the member sees it in the picker
  // before anything is sent.
  useEffect(() => {
    if (!enabled || recipient || peopleOnly || !(settled || rosterEmpty)) return;
    const { agent: fallback, from } = composerDefault({
      channelDefault,
      drivable: roomAgents,
      roomDefault: () => defaultRoomRecipient(roomAgents, targets.length > 0 || !!target, offered),
    });
    if (fallback) {
      defaulted.current = !settled;
      setChannelDefaulted(from === "channel" ? agentIdentity(fallback) : "");
      setRecipient(fallback);
    }
    // channelDefault is channelKey's identity.
  }, [enabled, settled, rosterEmpty, recipient, peopleOnly, roomAgents, offered, targets.length, target, channelKey]);

  // Save the pick (debounced; best-effort, never blocks sending) once the
  // stored one was read, so a default never overwrites it. A change pending
  // when the composer unmounts (leaving the room or channel) is flushed
  // instead of dropped. The channel's default, while it is the pick, is not
  // the member's choice and is not saved (decision: pending owner: a saved
  // copy would outlive a change of the channel's default).
  const isChannelDefault = !!recipient && !!channelDefaulted && agentIdentity(recipient) === channelDefaulted;
  const pick = enabled && settled && !isChannelDefault ? savedAgent(recipient, targets.find((t) => t.id === target), subject) : null;
  const snapshot = pick ? `${agentIdentity({ ...pick, core: pick.core || undefined })}/${model}/${reasoning}` : "";
  const lastSaved = useRef("");
  const pendingSave = useRef<{ readonly flush: () => void } | null>(null);
  useEffect(() => {
    if (!pick || snapshot === lastSaved.current) {
      pendingSave.current = null;
      return;
    }
    const body = { channelId: channelId ?? "", ...pick, model, reasoning };
    const flush = () => {
      pendingSave.current = null;
      lastSaved.current = snapshot;
      void api.putAgentPrefs(roomId, body).catch(() => {});
    };
    pendingSave.current = { flush };
    const t = window.setTimeout(flush, 1200);
    return () => window.clearTimeout(t);
    // pick, model and reasoning are snapshot's parts: snapshot is their identity.
  }, [snapshot, roomId, channelId]);
  useEffect(() => () => pendingSave.current?.flush(), []);

  return { agent, recipient, setRecipient, peopleOnly, setPeopleOnly, model, setModel, reasoning, setReasoning };
}
