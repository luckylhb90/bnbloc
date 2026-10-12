import { useState } from "react";
import { HashIcon, PlusIcon } from "@phosphor-icons/react";
import { api, type Channel } from "@/lib/api";
import { isSubmitEnter } from "@/lib/keys";
import { cn } from "@/lib/cn";

// ChannelRail is the in-room left rail listing the room's channels (default
// pinned first) and offering member-open channel creation. Selecting a channel
// re-scopes the room's message feed; the parent threads the chosen id into
// history, the event stream, chat sends, and read cursors.
export function ChannelRail({
  roomId,
  active,
  channels,
  loadFailed,
  reload,
  onSelect,
  unreadByChannel,
  collapsed = false,
}: {
  roomId: string;
  active: string | null;
  // The room's channel list and its loader live in the room
  // (useRoomChannels): the rail only renders them.
  channels: Channel[] | null;
  loadFailed: boolean;
  reload: () => Promise<void>;
  onSelect: (channelId: string) => void;
  // Unread count per channel id. Without it a member sees the room badge but
  // cannot tell which channel it came from.
  unreadByChannel?: Record<string, number>;
  // Icon-only mode: when a preview dock is open the chat column needs every
  // pixel it can get, so the rail sheds its labels (Slack's thread-panel
  // pattern). Active/unread state stays readable on the icon alone.
  collapsed?: boolean;
}) {
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const load = reload;

  const create = async () => {
    const n = name.trim();
    if (!n || busy) return;
    setBusy(true);
    try {
      const ch = await api.createChannel(roomId, n);
      setName("");
      setCreating(false);
      await load();
      onSelect(ch.channelId);
    } catch {
      // keep the form open on failure; the parent toasts on API errors
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className={
        collapsed
          ? "flex w-12 shrink-0 flex-col gap-0.5 border-r border-[var(--color-border)] p-1 transition-[width] duration-200 ease-out"
          : "flex w-44 shrink-0 flex-col gap-0.5 border-r border-[var(--color-border)] p-2 transition-[width] duration-200 ease-out"
      }
    >
      {loadFailed && (
        <button
          type="button"
          onClick={() => void load()}
          className="px-2 py-1 text-left text-xs text-[var(--color-danger)] hover:underline"
          title="Could not load channels — retry"
        >
          {collapsed ? "!" : "Couldn’t load channels — retry"}
        </button>
      )}
      {channels === null ? (
        <span className="px-2 py-1 text-xs text-[var(--color-fg-subtle)]">…</span>
      ) : (
        channels.map((c) => {
          const unread = unreadByChannel?.[c.channelId] ?? 0;
          // The active channel is being read right now, so its badge would flicker
          // as messages arrive and are immediately marked read.
          const showBadge = unread > 0 && c.channelId !== active;
          if (collapsed) {
            return (
              <button
                key={c.channelId}
                onClick={() => onSelect(c.channelId)}
                title={c.isDefault ? "The default channel" : c.name}
                className={cn(
                  "tap tactile relative flex h-9 w-9 items-center justify-center self-center rounded-[var(--radius)]",
                  c.channelId === active
                    ? "bg-[var(--color-elevated)] text-[var(--color-fg)]"
                    : "text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
                )}
              >
                <HashIcon size={16} />
                {showBadge && (
                  <span className="absolute -right-0.5 -top-0.5 h-2.5 w-2.5 rounded-full bg-[var(--color-accent)]" />
                )}
              </button>
            );
          }
          return (
            <button
              key={c.channelId}
              onClick={() => onSelect(c.channelId)}
              title={c.isDefault ? "The default channel" : c.name}
              className={cn(
                "tactile flex items-center gap-2 rounded-[var(--radius)] px-2 py-1.5 text-sm",
                c.channelId === active
                  ? "bg-[var(--color-elevated)] font-medium text-[var(--color-fg)]"
                  : showBadge
                    ? "font-medium text-[var(--color-fg)] hover:bg-[var(--color-surface-2)]"
                    : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
              )}
            >
              <HashIcon size={14} className="shrink-0 opacity-70" />
              <span className="flex-1 truncate text-left">{c.name}</span>
              {showBadge && (
                <span className="shrink-0 rounded-full bg-[var(--color-accent)] px-1.5 text-[11px] font-semibold leading-5 text-[var(--color-accent-fg,#04120c)]">
                  {unread > 99 ? "99+" : unread}
                </span>
              )}
            </button>
          );
        })
      )}
      {creating ? (
        <input
          autoFocus
          value={name}
          disabled={busy}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (isSubmitEnter(e)) void create();
            if (e.key === "Escape") setCreating(false);
          }}
          onBlur={() => name.trim() === "" && setCreating(false)}
          placeholder="channel name"
          className="mt-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1 text-sm"
        />
      ) : (
        <button
          onClick={() => setCreating(true)}
          title="New channel"
          className={
            collapsed
              ? "tap tactile mt-1 flex h-9 w-9 items-center justify-center self-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
              : "tactile mt-1 flex items-center gap-2 rounded-[var(--radius)] px-2 py-1.5 text-sm text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
          }
        >
          {collapsed ? <PlusIcon size={16} /> : (
            <>
              <PlusIcon size={14} /> New channel
            </>
          )}
        </button>
      )}
    </div>
  );
}
