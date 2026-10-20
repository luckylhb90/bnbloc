import { ShieldWarningIcon } from "@phosphor-icons/react";
import type { ContentComponent } from "@/lib/contentComponents";
import { scheduleLine, sourceAsk } from "@/lib/roomComponents";

const heading = "mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]";

// RoomSetup shows what a room component sets up in a new room: its
// channels, its scheduled tasks (created paused), the room expert, the
// packs and apps it turns on, the MCP tools they need, and the sources it
// asks to read, which nothing grants yet.
export function RoomSetup({ component }: { readonly component: Pick<ContentComponent, "room" | "requires"> }) {
  const room = component.room;
  const req = component.requires;
  if (!room) return null;
  const packs = [...(req.skills ?? []).map((r) => `skill pack ${r}`), ...(req.prompts ?? []).map((r) => `prompt pack ${r}`)];
  const rows: [string, readonly string[]][] = [
    ["Channels", room.channels.map((c) => `#${c}`)],
    ["Scheduled tasks (created paused)", room.schedules.map(scheduleLine)],
    ["Room expert", req.expert ? [req.expert] : []],
    ["Packs turned on", packs],
    ["Apps copied in", room.apps],
    ["MCP tools needed", (req.mcp ?? []).map((t) => t.name)],
  ];
  return (
    <section aria-label="Room setup" className="flex flex-col gap-3">
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
        <p key={s} className="flex items-start gap-1.5 text-xs text-[var(--color-fg-muted)]">
          <ShieldWarningIcon size={14} className="mt-0.5 shrink-0" aria-hidden="true" />
          {sourceAsk(s)}
        </p>
      ))}
    </section>
  );
}
