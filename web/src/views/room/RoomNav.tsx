import { dedupeMembers } from "@/lib/format";
import type { FeedItem } from "@/views/room/types";
import { useDisplayNames } from "@/lib/useDisplayName";

// OnlineStrip shows up to a few online members as overlapping avatars with a
// live green ring, plus a count, so the room surfaces who is present right now.
export function OnlineStrip({ online }: { online: string[] }) {
  const members = dedupeMembers(online);
  const nameOf = useDisplayNames(members);
  if (members.length === 0) return null;
  const shown = members.slice(0, 3);
  const extra = members.length - shown.length;
  return (
    <div
      className="mr-1 flex items-center"
      title={`Online: ${members.map(nameOf).join(", ")}`}
    >
      <div className="flex -space-x-2">
        {shown.map((u) => (
          <div
            key={u}
            className="grid size-7 place-items-center rounded-full border-2 border-[var(--color-accent)] bg-[var(--color-surface-2)] text-xs font-semibold text-[var(--color-fg-muted)]"
          >
            {(nameOf(u)[0] || "?").toUpperCase()}
          </div>
        ))}
      </div>
      {extra > 0 && (
        <span className="ml-1.5 text-xs text-[var(--color-fg-subtle)] tabular">
          +{extra}
        </span>
      )}
    </div>
  );
}

// MyMessagesNav is a floating right-edge navigator that lists the user's own
// messages so they can jump back to any of them. Collapsed it is a slim rail of
// dots; on hover it expands to show a text preview of each. Clicking scrolls the
// message into view and briefly flashes it. Hidden until there are 2+ own
// messages (nothing to navigate otherwise).
export function MyMessagesNav({ items }: { items: FeedItem[] }) {
  const mine = items.filter((m) => m.self);
  if (mine.length < 2) return null;

  const jump = (id: number) => {
    const el = document.getElementById(`msg-${id}`);
    if (!el) return;
    el.scrollIntoView({ behavior: "smooth", block: "center" });
    const bubble = el.firstElementChild as HTMLElement | null;
    if (bubble) {
      bubble.dataset.flash = "true";
      window.setTimeout(() => {
        delete bubble.dataset.flash;
      }, 1200);
    }
  };

  return (
    <div className="group pointer-events-none absolute right-2 top-1/2 z-10 -translate-y-1/2">
      <div className="pointer-events-auto flex max-h-[70vh] flex-col items-end gap-1 overflow-y-auto rounded-full border border-transparent px-1 py-2 transition-colors group-hover:border-[var(--color-border)] group-hover:bg-[var(--color-surface)]/80 group-hover:px-2 group-hover:backdrop-blur">
        {mine.map((m) => (
          <button
            key={m.id}
            onClick={() => jump(m.id)}
            title={m.text}
            className="tap tactile flex items-center gap-2 rounded-full text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
          >
            <span className="order-2 size-1.5 shrink-0 rounded-full bg-[var(--color-border-strong)] group-hover:bg-[var(--color-accent)]" />
            <span className="hover-reveal order-1 hidden max-w-[11rem] truncate text-xs group-hover:block">
              {m.text}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}
