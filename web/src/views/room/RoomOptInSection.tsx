import { useState } from "react";
import { errorText } from "@/lib/errors";
import { ROOM_SOURCE_LABELS } from "@/lib/roomSources";
import { refreshRoomSources, roomSourcesApi, useRoomSources } from "@/lib/roomSourcesApi";
import { RoomSourceValues } from "@/lib/wire.gen";
import { Switch } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";

// RoomOptInSection is the room owner's opt-in (room settings): whether the
// organization's reviews granted only to rooms that opt in may read this
// room's records. A grant to all rooms (or all but private ones) is the
// organization admin's alone; the header's note says who reviews the room.
export function RoomOptInSection({ roomId }: { readonly roomId: string }) {
  const toast = useToast();
  const { data } = useRoomSources(roomId);
  const [busy, setBusy] = useState(false);
  const optedIn = data?.optedIn ?? [];
  const save = async (source: (typeof RoomSourceValues)[number], on: boolean) => {
    setBusy(true);
    try {
      await roomSourcesApi.optIn(roomId, source, on);
      refreshRoomSources();
      toast.success(on ? "This room's conversations are opted in" : "This room's conversations are opted out");
    } catch (e) {
      toast.error(errorText(e, "Could not save the opt-in"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] p-4">
      <p className="text-sm font-medium">Organization reviews</p>
      <p className="mb-3 text-xs text-[var(--color-fg-muted)]">
        Your organization&apos;s admin may let a review room read {ROOM_SOURCE_LABELS.runs} of the rooms that opt in. Members see which room reviews
        this one.
      </p>
      {RoomSourceValues.map((s) => (
        <Switch
          key={s}
          checked={optedIn.includes(s)}
          disabled={busy || !data}
          onChange={(on) => void save(s, on)}
          label={`Opt this room's conversation outcomes in to reviews that ask rooms to opt in`}
          ariaLabel="Opt this room in to organization reviews"
        />
      ))}
    </div>
  );
}
