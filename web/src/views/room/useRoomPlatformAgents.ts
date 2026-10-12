import { useCallback, useEffect, useMemo, useState } from "react";
import { ApiError } from "@/lib/api";
import {
  addFailureMessage,
  platformChoices,
  samePlatformAgent,
  type LastHostLookup,
  type PlatformAgentRef,
  type PlatformChoice,
  type PlatformHost,
  type RoomPlatformAgent,
} from "@/lib/platformAgents";
import { platformAgentsApi } from "@/lib/platformAgentsApi";
import { pickHost, readLastHost, rememberHost } from "@/lib/platformRouting";
import { browserStorage } from "@/lib/storageKeys";
import { usePlatformAgents } from "@/lib/usePlatformAgents";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";

// Host online/queued state is re-read this often while the room is visible
// and has (or is offered) platform agents.
const POLL_MS = 20_000;

export interface RoomPlatformAgents {
  // Added to this room (drive them like borrowed agents).
  readonly inRoom: readonly RoomPlatformAgent[];
  // The composer picker's "Platform agents" group.
  readonly choices: readonly PlatformChoice[];
  // The host this room last used per agent (routing's first preference).
  readonly lastHost: LastHostLookup;
  // route picks the host a send to `agent` goes to now (null: every host is
  // offline); commit records it as this room's last host for the agent.
  readonly route: (agent: PlatformAgentRef) => PlatformHost | null;
  readonly commit: (agent: PlatformAgentRef, host: string) => void;
  // Re-reads the room's list and the organization's offer (after a refusal).
  readonly refresh: () => void;
  // Resolves true once the agent is in the room (toasts on failure).
  readonly add: (choice: PlatformChoice) => Promise<boolean>;
  readonly remove: (agent: RoomPlatformAgent) => Promise<void>;
}

// useRoomPlatformAgents owns a room's platform agents: the added list, the
// picker's choices (everything offered to the organization, with per-host
// availability), host routing, and add/remove. `gen` re-reads after a join.
export function useRoomPlatformAgents(roomId: string, subject: string | null, gen: number): RoomPlatformAgents {
  const toast = useToast();
  const { agents: offered, refetch: refetchOffered } = usePlatformAgents();
  const [inRoom, setInRoom] = useState<RoomPlatformAgent[]>([]);
  const [tick, setTick] = useState(0);
  // Bumped when a send records a host, so routed recipients follow it.
  const [routeGen, setRouteGen] = useState(0);

  useEffect(() => {
    let cancelled = false;
    platformAgentsApi
      .inRoom(roomId)
      .then((res) => !cancelled && setInRoom(res.agents))
      .catch(() => !cancelled && setInRoom([]));
    return () => {
      cancelled = true;
    };
  }, [roomId, gen, tick]);

  const refresh = useCallback(() => {
    setTick((n) => n + 1);
    refetchOffered();
  }, [refetchOffered]);
  useVisiblePoll(refresh, POLL_MS, inRoom.length > 0 || offered.length > 0);

  // A new lookup identity per routeGen re-routes the recipients built from it.
  const lastHost: LastHostLookup = useMemo(() => {
    void routeGen;
    return (agent: PlatformAgentRef) => readLastHost(browserStorage(), subject, roomId, agent);
  }, [subject, roomId, routeGen]);

  const choices = useMemo(() => platformChoices(offered, inRoom, subject, lastHost), [offered, inRoom, subject, lastHost]);

  // The same hosts the picker shows (the room's serving hosts with the live
  // offer's state, never the caller); the caller's own hosting only when it
  // is the sole host.
  const route = useCallback(
    (agent: PlatformAgentRef): PlatformHost | null => {
      const choice = choices.find((c) => samePlatformAgent({ core: c.agent.core ?? "", provider: c.agent.provider }, agent));
      const hosts = choice?.hosts ?? inRoom.find((a) => samePlatformAgent(a, agent))?.hosts ?? [];
      return pickHost(hosts, lastHost(agent));
    },
    [choices, inRoom, lastHost],
  );

  const commit = useCallback(
    (agent: PlatformAgentRef, host: string) => {
      if (readLastHost(browserStorage(), subject, roomId, agent) === host) return;
      rememberHost(browserStorage(), subject, roomId, agent, host);
      setRouteGen((n) => n + 1);
    },
    [subject, roomId],
  );

  const add = useCallback(
    async (choice: PlatformChoice): Promise<boolean> => {
      const agent = { core: choice.agent.core ?? "", provider: choice.agent.provider };
      try {
        const { agent: added } = await platformAgentsApi.add(roomId, agent);
        // Re-read the room's list: the add may also have dropped stale ones.
        const listed = await platformAgentsApi.inRoom(roomId).catch(() => null);
        if (listed) setInRoom(listed.agents);
        else setInRoom((cur) => [...cur.filter((a) => !samePlatformAgent(a, added)), { ...added, hosts: added.hosts ?? choice.hosts }]);
        toast.success(`${agent.provider} added to this room`);
        return true;
      } catch (e) {
        const status = e instanceof ApiError ? e.status : 0;
        toast.error(addFailureMessage(status, errorText(e, ""), agent.provider));
        // An agent that is no longer published drops out of the picker.
        if (status === 404) refetchOffered();
        return false;
      }
    },
    [roomId, toast, refetchOffered],
  );

  const remove = useCallback(
    async (agent: RoomPlatformAgent) => {
      try {
        await platformAgentsApi.remove(roomId, agent);
        setInRoom((cur) => cur.filter((a) => !samePlatformAgent(a, agent)));
        toast.success(`Removed ${agent.provider} from this room`);
      } catch (e) {
        toast.error(errorText(e, "Remove failed"));
      }
    },
    [roomId, toast],
  );

  return { inRoom, choices, lastHost, route, commit, refresh, add, remove };
}
