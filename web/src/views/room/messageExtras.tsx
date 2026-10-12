import { createContext, useContext } from "react";
import { BookmarkSimpleIcon, CheckSquareIcon, ListChecksIcon } from "@phosphor-icons/react";
import type { MessageTask, TaskStatus } from "@/lib/api";
import { cn } from "@/lib/cn";
import { taskSeq } from "@/lib/team";
import type { FeedItem } from "@/views/room/types";
import { useDisplayName } from "@/lib/useDisplayName";

// Team actions on a feed row — "Save as prompt" (prompt packs) and "Mark as
// task" (F6)
// — plus the task badge. They reach the memoized Message rows through context
// so the row props (and the existing copy / edit / delete actions) stay as
// they are; only these small components re-render when tasks change.
export interface MessageExtras {
  readonly taskBySeq: ReadonlyMap<number, MessageTask>;
  readonly onSaveAsPrompt: (item: FeedItem) => void;
  readonly onMarkTask: (seq: number) => void;
  readonly onUnmarkTask: (seq: number) => void;
  readonly onSetTaskStatus: (seq: number, status: TaskStatus) => void;
}

const MessageExtrasContext = createContext<MessageExtras | null>(null);
export const MessageExtrasProvider = MessageExtrasContext.Provider;

// Only persisted rows (a server sequence, not pending/failed) can be saved or
// marked: a task points at a stored message.
function persistedSeq(item: FeedItem): number | null {
  if (item.status === "pending" || item.status === "failed") return null;
  return taskSeq(item.seq);
}

const iconButton =
  "tactile flex items-center text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60 rounded-sm";

// MessageExtraActions renders the two team actions as small icon buttons that
// sit beside the existing copy control. `floating` places them in a hover
// cluster at the row's top-right (grouped rows have no header to host them),
// absolutely positioned so the feed layout never shifts.
export function MessageExtraActions({ item, floating }: { item: FeedItem; floating?: boolean }) {
  const extras = useContext(MessageExtrasContext);
  const seq = persistedSeq(item);
  if (!extras || seq == null) return null;
  const task = extras.taskBySeq.get(seq);
  return (
    <span
      className={cn(
        "flex items-center gap-1.5",
        floating &&
          "absolute right-0 top-0 z-[1] rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-1.5 py-1 opacity-0 shadow-[var(--shadow-pop)] transition-opacity group-hover:opacity-100 focus-within:opacity-100",
      )}
    >
      {item.text && (
        <button
          type="button"
          onClick={() => extras.onSaveAsPrompt(item)}
          title="Save as prompt"
          aria-label="Save as prompt"
          className={iconButton}
        >
          <BookmarkSimpleIcon size={12} />
        </button>
      )}
      <button
        type="button"
        onClick={() => (task ? extras.onUnmarkTask(seq) : extras.onMarkTask(seq))}
        title={task ? "Remove task" : "Mark as task"}
        aria-label={task ? "Remove task" : "Mark as task"}
        className={cn(iconButton, task && "text-[var(--color-accent)]")}
      >
        <ListChecksIcon size={12} weight={task ? "bold" : "regular"} />
      </button>
    </span>
  );
}

// TaskBadge marks a task message; clicking it toggles open/done.
export function TaskBadge({ item, className }: { item: FeedItem; className?: string }) {
  const extras = useContext(MessageExtrasContext);
  const seq = persistedSeq(item);
  const task = seq != null ? extras?.taskBySeq.get(seq) : undefined;
  const doneBy = useDisplayName(task?.doneBy);
  if (!extras || seq == null || !task) return null;
  const done = task.status === "done";
  return (
    <button
      type="button"
      onClick={() => extras.onSetTaskStatus(seq, done ? "open" : "done")}
      title={done ? `Done${doneBy ? ` by ${doneBy}` : ""} — mark as open` : "Open task — mark as done"}
      className={cn(
        "tactile inline-flex items-center gap-1 rounded-full border px-1.5 text-[0.65rem] font-medium leading-4",
        done
          ? "border-[var(--color-border)] text-[var(--color-fg-subtle)] line-through"
          : "border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] text-[var(--color-accent)]",
        className,
      )}
    >
      <CheckSquareIcon size={10} weight={done ? "fill" : "regular"} />
      {done ? "Done" : "Task"}
    </button>
  );
}
