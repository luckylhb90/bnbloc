import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { shouldShowDesktopHandoff } from "@/lib/desktopHandoff";
import { useDesktopPresence } from "@/lib/desktopPresence";

// useDesktopHandoff decides whether the empty room offers the desktop app: the
// member's own connection state comes from the app-wide DesktopPresence; this
// hook only adds the room roster (an own agent already in the room hides the
// card) and feeds that roster to the room.
export function useDesktopHandoff(
  roomId: string,
  subject: string | null,
  onRoomAgents: (agents: { ownerUserId: string; core?: string; provider: string }[]) => void,
) {
  const presence = useDesktopPresence();
  const [roomAgents, setRoomAgents] = useState<
    { ownerUserId: string; core?: string; provider: string }[] | undefined
  >();
  // Only the latest roster fetch may apply its answer.
  const generation = useRef(0);
  useEffect(() => () => void (generation.current += 1), []);

  const loadRoster = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const room = await api.roomAgents(roomId);
      if (mine !== generation.current) return;
      setRoomAgents(room.agents);
      onRoomAgents(room.agents || []);
    } catch {
      if (mine === generation.current) setRoomAgents(undefined);
    }
  }, [onRoomAgents, roomId]);

  useEffect(() => {
    setRoomAgents(undefined);
    if (presence.applicable) void loadRoster();
  }, [loadRoster, presence.applicable, subject]);

  const show = shouldShowDesktopHandoff({
    subject,
    inDesktopShell: !presence.applicable,
    userAgent: "",
    ownAgents: presence.ownAgents,
    roomAgents,
    roomRosterKnown: roomAgents !== undefined,
  });

  return { show };
}
