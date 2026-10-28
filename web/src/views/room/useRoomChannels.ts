import { useCallback, useEffect, useRef, useState } from "react";
import type { Channel } from "@/lib/api";
import { channelsApi } from "@/lib/channelsApi";
import type { ChannelGroup, RoomSection } from "@/lib/wire.gen";

export interface RoomChannels {
  // null until the first load answers.
  readonly channels: Channel[] | null;
  readonly sections: readonly RoomSection[];
  // The member's own groups of their rail (C24).
  readonly groups: readonly ChannelGroup[];
  // The room's archived channels (the rail's "Archived" list).
  readonly archived: readonly Channel[];
  // The open channel's setup (null until known).
  readonly current: Channel | null;
  readonly loadFailed: boolean;
  readonly reload: () => Promise<void>;
}

// useRoomChannels owns the room's channel list (the open channels with the
// member's own following, and the sections) and the default-channel
// selection. It lives in the room (not in the rail that renders the list):
// on a narrow room the rail is a drawer that is only mounted while open, and
// a selection made by the rail never happened there — the feed stayed empty
// until the member opened the drawer. A channel_updated event (any change to
// a channel or a section of the room) calls reload.
export function useRoomChannels(
  roomId: string,
  epoch: number,
  active: string | null,
  onSelect: (channelId: string) => void,
): RoomChannels {
  const [channels, setChannels] = useState<Channel[] | null>(null);
  const [sections, setSections] = useState<readonly RoomSection[]>([]);
  const [groups, setGroups] = useState<readonly ChannelGroup[]>([]);
  const [archived, setArchived] = useState<readonly Channel[]>([]);
  const [loadFailed, setLoadFailed] = useState(false);
  // The list is fetched per ROOM, never per parent render: the selection
  // callback is read through a ref so an unstable callback cannot refetch.
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const generation = useRef(0);

  const reload = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const [res, old] = await Promise.all([channelsApi.list(roomId), channelsApi.list(roomId, true)]);
      if (mine !== generation.current) return;
      setChannels(res.channels ?? []);
      setSections(res.sections ?? []);
      setGroups(res.groups ?? []);
      setArchived(old.channels ?? []);
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

  // Select the room's default channel (listed first, isDefault) once known.
  const defaultChannel = channels?.find((c) => c.isDefault)?.channelId ?? null;
  useEffect(() => {
    if (!active && defaultChannel) onSelectRef.current(defaultChannel);
  }, [active, defaultChannel]);

  // The open channel, by its id: an open one, or an archived one the member
  // still has open (read-only, the composer says why).
  const current = (active && (channels?.find((c) => c.channelId === active) ?? archived.find((c) => c.channelId === active))) || null;

  return { channels, sections, groups, archived, current, loadFailed, reload };
}
