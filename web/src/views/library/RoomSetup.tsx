import { ShieldWarningIcon } from "@phosphor-icons/react";
import type { ContentComponent } from "@/lib/contentComponents";
import { formatBytes } from "@/lib/format";
import { scheduleLine } from "@/lib/roomComponents";
import { sourceAsk } from "@/lib/roomSources";
import { RoomLayoutPreview } from "@/views/RoomLayoutPreview";

const heading = "mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]";

// RoomSetup shows what a room component sets up in a new room: its
// channel layout (the rail it makes), its scheduled tasks (created paused), the room expert, the
// packs, apps and files it turns on or copies in, the MCP tools they need, and the sources it
// asks to read with their purpose (a room made from it waits for its
// organization admin's consent).
export function RoomSetup({ component }: { readonly component: Pick<ContentComponent, "room" | "requires"> }) {
  const room = component.room;
  const req = component.requires;
  if (!room) return null;
  const packs = [...(req.skills ?? []).map((r) => `skill pack ${r}`), ...(req.prompts ?? []).map((r) => `prompt pack ${r}`)];
  const rows: [string, readonly string[]][] = [
    ["Scheduled tasks (created paused)", room.schedules.map(scheduleLine)],
    ["Room expert", req.expert ? [req.expert] : []],
    ["Packs turned on", packs],
    ["Apps copied in", [...room.apps, ...room.appSnapshots.map((a) => a.title)]],
    ["Files copied in", room.files.map((f) => `${f.name} (${formatBytes(f.sizeBytes)})`)],
    ["MCP tools needed", (req.mcp ?? []).map((t) => t.name)],
  ];
  return (
    <section aria-label="Room setup" className="flex flex-col gap-3">
      {room.channels.length + room.sections.length > 0 && (
        <div>
          <h3 className={heading}>Channels</h3>
          <RoomLayoutPreview layout={room} />
        </div>
      )}
      {rows
        .filter(([, items]) => items.length > 0)
        .map(([title, items]) => (
          <div key={title}>
            <h3 className={heading}>{title}</h3>
            <ul className="flex flex-col gap-0.5 text-xs">
              {items.map((it) => (
                <li key={it} className="truncate" title={it}>
                  {it}
                </li>
              ))}
            </ul>
          </div>
        ))}
      {room.sources.map((s) => (
        <p key={s.name} className="flex items-start gap-1.5 text-xs text-[var(--color-fg-muted)]">
          <ShieldWarningIcon size={14} className="mt-0.5 shrink-0" aria-hidden="true" />
          {sourceAsk(s)}
        </p>
      ))}
    </section>
  );
}
