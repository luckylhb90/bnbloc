import { useCallback, useEffect, useRef, useState } from "react";
import { api, type Channel } from "@/lib/api";

export interface RoomChannels {
  // null until the first load answers.
  readonly channels: Channel[] | null;
  readonly loadFailed: boolean;
  readonly reload: () => Promise<void>;
}

// useRoomChannels owns the room's channel list and the default-channel
// selection. It lives in the room (not in the rail that renders the list):
// on a narrow room the rail is a drawer that is only mounted while open, and
// a selection made by the rail never happened there — the feed stayed empty
// until the member opened the drawer.
export function useRoomChannels(
  roomId: string,
  epoch: number,
  active: string | null,
  onSelect: (channelId: string) => void,
): RoomChannels {
  const [channels, setChannels] = useState<Channel[] | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  // The list is fetched per ROOM, never per parent render: the selection
  // callback is read through a ref so an unstable callback cannot refetch.
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const generation = useRef(0);

  const reload = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const res = await api.channels(roomId);
      if (mine !== generation.current) return;
      setChannels(res.channels ?? []);
      setLoadFailed(false);
    } catch {
      if (mine !== generation.current) return;
      setLoadFailed(true);
      setChannels((cur) => cur ?? []);
    }
  }, [roomId]);

  useEffect(() => {
    if (epoch > 0) setChannels(null);
    void reload();
    return () => {
      generation.current += 1;
    };
  }, [reload, epoch]);

  // Select the room's default channel (listed first) once known.
  const firstChannel = channels?.[0]?.channelId ?? null;
  useEffect(() => {
    if (!active && firstChannel) onSelectRef.current(firstChannel);
  }, [active, firstChannel]);

  return { channels, loadFailed, reload };
}
