import { useState } from "react";
import { ArchiveIcon, HashIcon, ScrollIcon } from "@phosphor-icons/react";
import type { Channel, RoomDetail } from "@/lib/api";
import type { RoomSection } from "@/lib/wire.gen";
import { Dialog } from "@/ui/Dialog";
import { Button } from "@/ui/Button";

// ChannelHeader is the open channel's line above the feed
// (docs/prd/2026-10-28-channels-and-threads.md §2.5a): its name, its
// purpose, and a "Rules" link to one sheet with the room's, the section's
// and the channel's rules. An archived channel says so.
export function ChannelHeader({ channel, section, room }: { readonly channel: Channel | null; readonly section: RoomSection | null; readonly room: RoomDetail | null }) {
  const [rulesOpen, setRulesOpen] = useState(false);
  if (!channel) return null;
  const hasRules = !!(room?.rules || section?.rules || channel.rules);
  return (
    <div data-channel-header className="flex min-w-0 items-center gap-2 border-b border-[var(--color-border)] px-4 py-1.5 text-sm">
      <HashIcon size={14} className="shrink-0 text-[var(--color-fg-subtle)]" />
      <span className="shrink-0 font-medium">{channel.name}</span>
      {channel.archivedAtMs ? (
        <span className="flex shrink-0 items-center gap-1 rounded-full bg-[var(--color-surface-2)] px-2 text-xs text-[var(--color-fg-muted)]">
          <ArchiveIcon size={12} /> Archived
        </span>
      ) : null}
      <span className="min-w-0 flex-1 truncate text-[var(--color-fg-muted)]" title={channel.purpose}>
        {channel.purpose}
      </span>
      {hasRules && (
        <Button size="sm" variant="ghost" onClick={() => setRulesOpen(true)} className="shrink-0">
          <ScrollIcon size={14} /> Rules
        </Button>
      )}
      {rulesOpen && <RulesSheet room={room} section={section} channel={channel} onClose={() => setRulesOpen(false)} />}
    </div>
  );
}

// RulesSheet shows the rules that hold in a channel: the room's, its
// section's and its own, in that order.
export function RulesSheet({ room, section, channel, onClose }: { readonly room: RoomDetail | null; readonly section: RoomSection | null; readonly channel: Channel | null; readonly onClose: () => void }) {
  const parts = [
    { title: "The room", text: room?.rules ?? "" },
    { title: section ? `The section ${section.name}` : "", text: section?.rules ?? "" },
    { title: channel ? `#${channel.name}` : "", text: channel?.rules ?? "" },
  ].filter((p) => p.title && p.text.trim());
  return (
    <Dialog open onClose={onClose} title="Rules">
      {parts.length === 0 ? (
        <p className="text-sm text-[var(--color-fg-subtle)]">No rules here.</p>
      ) : (
        <div className="flex flex-col gap-4">
          {parts.map((p) => (
            <section key={p.title}>
              <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">{p.title}</h3>
              <p className="whitespace-pre-wrap text-sm">{p.text}</p>
            </section>
          ))}
        </div>
      )}
    </Dialog>
  );
}
