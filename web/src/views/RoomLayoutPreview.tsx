import { CaretDownIcon, HashIcon } from "@phosphor-icons/react";
import { layoutGroups, type RoomLayout } from "@/lib/roomLayout";

// RoomLayoutPreview shows the rail a layout makes in a new room
// (docs/prd/2026-10-28-channels-and-threads.md C25): the default channel
// and the top-level channels, then each section with its channels, each
// with its purpose. Read-only: nothing is made until the room is created.
export function RoomLayoutPreview({ layout }: { readonly layout: RoomLayout }) {
  return (
    <div data-layout-preview className="flex flex-col gap-0.5 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-2 text-sm">
      {layoutGroups(layout).map((g) => (
        <div key={g.section?.name ?? ""} data-preview-section={g.section?.name ?? ""} className="flex flex-col gap-0.5">
          {g.section && (
            <p title={g.section.rules || undefined} className="mt-1.5 flex items-center gap-1 px-1 text-[0.7rem] font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
              <CaretDownIcon size={11} aria-hidden="true" />
              <span className="truncate">{g.section.name}</span>
            </p>
          )}
          {g.channels.map((c) => (
            <p key={c.name} data-preview-channel={c.name} className="flex min-w-0 items-baseline gap-2 px-2 py-0.5">
              <HashIcon size={13} className="shrink-0 self-center opacity-70" aria-hidden="true" />
              <span className="shrink-0 text-[var(--color-fg)]">{c.name}</span>
              {c.purpose && <span className="truncate text-xs text-[var(--color-fg-subtle)]">{c.purpose}</span>}
            </p>
          ))}
        </div>
      ))}
    </div>
  );
}
