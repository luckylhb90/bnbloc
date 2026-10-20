import { useCallback, useEffect, useState } from "react";
import { ArrowClockwiseIcon } from "@phosphor-icons/react";
import { errorText } from "@/lib/errors";
import { exactTime, formatDate } from "@/lib/format";
import { ROOM_SOURCE_LABELS, sourceState, type RoomSourceGrant } from "@/lib/roomSources";
import { roomSourcesApi } from "@/lib/roomSourcesApi";
import { ErrorBox } from "@/ui/ErrorBox";
import { listPlaceholder } from "@/ui/QueryState";
import { UserName } from "@/views/UserName";
import { RoomSourceDialog } from "@/views/org/RoomSourceDialog";

// RoomSourcesTab is the organization admin's list of rooms that ask to
// read organization records (docs/prd/2026-10-20-rooms-as-components.md
// §2.3): each room, the component it was installed from, its stated
// purpose and where the ask stands. "Decide…" opens the grant dialog
// (the scope, an AI explanation, grant or withdraw).
export function RoomSourcesTab() {
  const [grants, setGrants] = useState<RoomSourceGrant[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState<RoomSourceGrant | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setGrants((await roomSourcesApi.grants()).grants ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load the room sources"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-[var(--color-fg-muted)]">Rooms whose scheduled tasks ask to read records of the whole organization. Nothing is read until you grant it.</p>
        <button
          type="button"
          onClick={() => void load()}
          className="tactile inline-flex items-center gap-1 rounded-[var(--radius)] px-2 py-1 text-sm text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
        >
          <ArrowClockwiseIcon size={14} /> Refresh
        </button>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      <div className="overflow-x-auto rounded-[var(--radius)] border border-[var(--color-border)]">
        <table className="w-full min-w-[40rem] text-sm">
          <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
            <tr>
              <th className="px-3 py-2">Room</th>
              <th className="px-3 py-2">Reads</th>
              <th className="px-3 py-2">Purpose</th>
              <th className="px-3 py-2">Asked</th>
              <th className="px-3 py-2">State</th>
              <th className="px-3 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {grants && grants.length > 0 ? (
              grants.map((g) => (
                <tr key={`${g.roomId}:${g.source}`} className="border-t border-[var(--color-border)] align-top">
                  <td className="max-w-[12rem] px-3 py-2">
                    <span className="block truncate font-medium" title={g.roomId}>
                      {g.roomName}
                    </span>
                    <span className="text-xs text-[var(--color-fg-subtle)]">{g.from}</span>
                  </td>
                  <td className="px-3 py-2 text-[var(--color-fg-muted)]">{ROOM_SOURCE_LABELS[g.source]}</td>
                  <td className="max-w-[18rem] px-3 py-2 text-[var(--color-fg-muted)]">
                    <span className="line-clamp-3" title={g.purpose}>
                      {g.purpose}
                    </span>
                  </td>
                  <td className="px-3 py-2 text-xs text-[var(--color-fg-muted)]" title={exactTime(g.askedAtMs)}>
                    <UserName id={g.askedBy} />
                    <span className="block">{formatDate(g.askedAtMs)}</span>
                  </td>
                  <td className="px-3 py-2 text-xs text-[var(--color-fg-muted)]">{sourceState(g.scope)}</td>
                  <td className="px-3 py-2 text-right">
                    <button
                      type="button"
                      onClick={() => setOpen(g)}
                      className="tactile rounded-[var(--radius)] px-2 py-1 text-xs text-[var(--color-accent)] hover:bg-[var(--color-surface-2)]"
                    >
                      Decide…
                    </button>
                  </td>
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={6} className="px-3 py-6 text-center text-[var(--color-fg-subtle)]">
                  {listPlaceholder(grants, !!error, "No room asks to read organization records.")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {open && <RoomSourceDialog grant={open} onClose={() => setOpen(null)} onDecided={() => void load()} />}
    </section>
  );
}
