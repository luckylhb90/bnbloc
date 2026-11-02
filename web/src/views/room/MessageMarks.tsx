import { useContext, useState } from "react";
import { BookmarkSimpleIcon, CaretDownIcon, CaretUpIcon, PushPinIcon, SmileyIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { isPinned, isSaved, pinnedIn, QUICK_REACTIONS, reactionsOf } from "@/lib/messageMarks";
import type { MessageMark } from "@/lib/wire.gen";
import { Menu } from "@/ui/Menu";
import { MessageExtrasContext } from "@/views/room/messageExtras";
import type { FeedItem } from "@/views/room/types";

const iconButton =
  "tactile flex items-center text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60 rounded-sm";

// MarkActions are a stored message's reaction picker, pin (for who manages
// the channel) and save (the member's own), beside its other actions.
export function MarkActions({ seq }: { readonly seq: number }) {
  const extras = useContext(MessageExtrasContext);
  if (!extras?.onSetMark || !extras.marks) return null;
  const set = extras.onSetMark;
  const pinned = isPinned(extras.marks, seq);
  const saved = isSaved(extras.marks, seq, extras.me ?? null);
  const mine = new Set(reactionsOf(extras.marks, seq, extras.me ?? null).filter((r) => r.mine).map((r) => r.value));
  return (
    <>
      <Menu
        label="Add a reaction"
        menuClassName="absolute right-0 top-full mt-1 w-auto"
        trigger={(props) => (
          <button {...props} data-react title="Add a reaction" aria-label="Add a reaction" className={iconButton}>
            <SmileyIcon size={12} />
          </button>
        )}
      >
        <div className="flex gap-1 px-2 py-1.5">
          {QUICK_REACTIONS.map((emoji) => (
            <button
              key={emoji}
              type="button"
              role="menuitem"
              data-reaction-choice={emoji}
              onClick={() => set(seq, "reaction", !mine.has(emoji), emoji)}
              className={cn("tactile grid size-8 place-items-center rounded-[var(--radius)] text-base hover:bg-[var(--color-surface-2)]", mine.has(emoji) && "bg-[var(--color-accent-soft)]")}
            >
              {emoji}
            </button>
          ))}
        </div>
      </Menu>
      {extras.mayPin && (
        <button
          type="button"
          data-pin
          onClick={() => set(seq, "pin", !pinned)}
          title={pinned ? "Unpin" : "Pin to this channel"}
          aria-label={pinned ? "Unpin" : "Pin to this channel"}
          className={cn(iconButton, pinned && "text-[var(--color-accent)]")}
        >
          <PushPinIcon size={12} weight={pinned ? "fill" : "regular"} />
        </button>
      )}
      <button
        type="button"
        data-save
        onClick={() => set(seq, "saved", !saved)}
        title={saved ? "Remove from saved" : "Save for later"}
        aria-label={saved ? "Remove from saved" : "Save for later"}
        className={cn(iconButton, saved && "text-[var(--color-accent)]")}
      >
        <BookmarkSimpleIcon size={12} weight={saved ? "fill" : "regular"} />
      </button>
    </>
  );
}

// Reactions is a message's reactions under it: each emoji with its count;
// a click adds or takes off the member's own.
export function Reactions({ item, className }: { readonly item: FeedItem; readonly className?: string }) {
  const extras = useContext(MessageExtrasContext);
  const seq = item.status === "pending" || item.status === "failed" ? null : (item.seq ?? null);
  if (seq == null || !extras?.marks) return null;
  const list = reactionsOf(extras.marks, seq, extras.me ?? null);
  if (list.length === 0) return null;
  const set = extras.onSetMark;
  return (
    <div data-reactions className={cn("flex flex-wrap gap-1", className)}>
      {list.map((r) => (
        <button
          key={r.value}
          type="button"
          data-reaction={r.value}
          disabled={!set}
          onClick={() => set?.(seq, "reaction", !r.mine, r.value)}
          aria-pressed={r.mine}
          className={cn(
            "tactile inline-flex items-center gap-1 rounded-full border px-1.5 text-xs leading-5",
            r.mine ? "border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] text-[var(--color-fg)]" : "border-[var(--color-border)] text-[var(--color-fg-muted)] enabled:hover:text-[var(--color-fg)]",
          )}
        >
          <span>{r.value}</span>
          <span data-reaction-count>{r.count}</span>
        </button>
      ))}
    </div>
  );
}

// PinnedStrip is the open channel's pinned messages: a pill at the top-left
// of the conversation that opens their list; a row jumps to its message.
export function PinnedStrip({
  marks,
  channelId,
  feed,
  onJump,
}: {
  readonly marks: readonly MessageMark[];
  readonly channelId: string | null;
  readonly feed: readonly FeedItem[];
  readonly onJump: (seq: number, channelId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const pins = pinnedIn(marks, channelId);
  if (pins.length === 0) return null;
  const textOf = (seq: number) => feed.find((f) => f.seq === seq)?.text.split("\n").find((l) => l.trim()) ?? `Message #${seq}`;
  return (
    <div className="pointer-events-none absolute left-4 top-2 z-10 flex max-w-[calc(50%-1rem)] flex-col items-start @5xl:left-6">
      <button
        type="button"
        data-pinned-strip
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="tactile pointer-events-auto flex items-center gap-1.5 rounded-full border border-[var(--color-border)] bg-[var(--color-surface)]/90 px-2.5 py-1 text-xs text-[var(--color-fg-muted)] shadow-[var(--shadow-pop)] backdrop-blur hover:text-[var(--color-fg)]"
      >
        <PushPinIcon size={13} className="text-[var(--color-accent)]" />
        {pins.length} pinned
        {open ? <CaretUpIcon size={11} /> : <CaretDownIcon size={11} />}
      </button>
      {open && (
        <ul className="pointer-events-auto mt-1.5 max-h-72 w-80 max-w-full overflow-y-auto rounded-[var(--radius-lg)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] py-1 shadow-[var(--shadow-diffuse)]">
          {pins.map((p) => (
            <li key={p.seq}>
              <button
                type="button"
                data-pinned-row={p.seq}
                onClick={() => {
                  onJump(p.seq, p.channelId ?? "");
                  setOpen(false);
                }}
                className="tactile block w-full truncate px-3 py-2 text-left text-sm text-[var(--color-fg)] hover:bg-[var(--color-surface-2)]"
              >
                {textOf(p.seq)}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
