import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, ApiError, type Member, type Room } from "@/lib/api";
import { dedupeMembers, sameUser } from "@/lib/format";
import { isRoomGoneStatus } from "@/lib/roomGone";
import { composeRoomAgents } from "@/lib/platformAgents";
import { catalogRefreshKey, type RoomAgentCatalog, type RoomModelCatalogs } from "@/lib/agentCatalogs";
import type { RosterAgent, SharedAgent } from "@/lib/roomAgents";
import { useToast } from "@/ui/Toast";
import type { RoomAgent } from "@/views/composer/types";
import { useRoomPlatformAgents } from "@/views/room/useRoomPlatformAgents";

// useRoomData owns the room-level server state: the room detail, its members
// (and so the signed-in member's own membership, `me`: the one role source),
// the live agent roster, the borrowable (authorized) agents and the added
// platform agents, the agents' model catalogs, plus joining.
// It is room-scoped (the room view is keyed by room id) and independent of the
// active channel, so switching channel never refetches or blanks it. Every
// load is cancelled when superseded (re-join) or unmounted. A room detail
// answered 404/403 (deleted, or no longer visible) calls onGone once instead
// of leaving the view loading a room that will never load.
export function useRoomData({
  roomId,
  subject,
  reauth,
  onGone,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly reauth: () => Promise<void>;
  readonly onGone: () => void;
}) {
  const onGoneRef = useRef(onGone);
  onGoneRef.current = onGone;
  const toast = useToast();
  const [room, setRoom] = useState<Room | null>(null);
  // null until the member list first answers.
  const [people, setPeople] = useState<Member[] | null>(null);
  const [agentHosts, setAgentHosts] = useState<string[]>([]);
  // Bumped by reloadMembers (a member_joined / member_removed event).
  const [membersGen, setMembersGen] = useState(0);
  const [roster, setRoster] = useState<RosterAgent[]>([]);
  const [shared, setShared] = useState<SharedAgent[]>([]);
  const [joinedGen, setJoinedGen] = useState(0);
  const [joining, setJoining] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const guard =
      <T,>(apply: (value: T) => void) =>
      (value: T) => {
        if (!cancelled) apply(value);
      };
    api
      .getRoom(roomId)
      .then(guard(setRoom))
      .catch(
        guard((e: unknown) => {
          if (e instanceof ApiError && isRoomGoneStatus(e.status)) onGoneRef.current();
        }),
      );
    api
      .roomAgents(roomId)
      .then(guard((res) => setRoster(res.agents || [])))
      .catch(guard(() => setRoster([])));
    api
      .sharedAgents(roomId)
      .then(
        guard((list) =>
          setShared((list || []).map((a) => ({ ownerUserId: a.ownerUserId, provider: a.provider, allowedModels: a.allowedModels ?? [] }))),
        ),
      )
      .catch(guard(() => setShared([])));
    return () => {
      cancelled = true;
    };
  }, [roomId, joinedGen]);

  useEffect(() => {
    let cancelled = false;
    api
      .members(roomId)
      .then((m) => {
        if (cancelled) return;
        setPeople(m.members);
        setAgentHosts(m.agentHosts);
      })
      .catch(() => {
        if (cancelled) return;
        setPeople([]);
        setAgentHosts([]);
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, joinedGen, membersGen]);
  const reloadMembers = useCallback(() => setMembersGen((g) => g + 1), []);
  const members = useMemo(() => dedupeMembers((people ?? []).map((x) => x.userId)), [people]);
  // The signed-in member's membership: undefined while loading, null when
  // not a member (a preview, or just removed).
  const me = useMemo(() => (people === null ? undefined : (people.find((m) => sameUser(m.userId, subject)) ?? null)), [people, subject]);

  const platform = useRoomPlatformAgents(roomId, subject, joinedGen);
  // The selectable agents: one stable array per (roster, grants, platform
  // agents, subject), so consumers (composer, borrow dialog) can depend on it
  // without refetching.
  const roomAgents: RoomAgent[] = useMemo(
    () => composeRoomAgents({ roster, shared, platform: platform.inRoom, agentHosts, subject, lastHost: platform.lastHost }),
    [roster, shared, platform.inRoom, agentHosts, subject, platform.lastHost],
  );

  // The owner-keyed model catalogs of the room's agents, re-read whenever
  // the addressable agent set changes (roster event, grant, platform agent).
  const [catalogs, setCatalogs] = useState<RoomAgentCatalog[]>([]);
  const catalogKey = catalogRefreshKey(roomAgents);
  useEffect(() => {
    let cancelled = false;
    api
      .roomAgentCatalogs(roomId)
      .then((res) => !cancelled && setCatalogs(res.agents ?? []))
      .catch(() => !cancelled && setCatalogs([]));
    return () => {
      cancelled = true;
    };
  }, [roomId, joinedGen, catalogKey]);
  const modelCatalogs: RoomModelCatalogs = useMemo(() => ({ catalogs, grants: shared }), [catalogs, shared]);

  // join adds the user to a shared room they are previewing, refreshes the
  // session token so it carries the new room scope, reloads the room detail
  // (the composer replaces the join panel) and re-subscribes the stream.
  const join = useCallback(async () => {
    setJoining(true);
    try {
      await api.joinRoom(roomId);
      await reauth();
      setRoom(await api.getRoom(roomId));
      setJoinedGen((g) => g + 1);
      toast.success("Joined room");
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : "Could not join room");
    } finally {
      setJoining(false);
    }
  }, [roomId, reauth, toast]);

  return { room, setRoom, members, me, reloadMembers, roomAgents, modelCatalogs, platform, setRoster, joinedGen, joining, join };
}
