import { createContext, useContext } from "react";
import { ArrowBendUpLeftIcon, BookmarkSimpleIcon, ChatsCircleIcon, CheckSquareIcon, ListChecksIcon } from "@phosphor-icons/react";
import { rootLine, type ThreadSummary } from "@/lib/threads";
import type { MessageTask, TaskStatus } from "@/lib/api";
import { cn } from "@/lib/cn";
import type { FeedItem } from "@/views/room/types";
import { useDisplayName } from "@/lib/useDisplayName";
import { FindAWayButton } from "@/views/room/DeadEndCard";

// Team actions on a feed row — "Save as prompt" (prompt packs), "Mark as
// task" (F6) and an agent reply's "Find a way" (DeadEndCard.tsx)
// — plus the task badge. They reach the memoized Message rows through context
// so the row props (and the existing copy / edit / delete actions) stay as
// they are; only these small components re-render when tasks change.
export interface MessageExtras {
  readonly taskBySeq: ReadonlyMap<number, MessageTask>;
  // Threads (channels PRD §2.2): the threads of this feed's channel by their
  // root's sequence, and opening one (a message's "Reply in thread", a
  // root's line). Absent where threads do not open (the people chat, a
  // thread's own panel).
  readonly threadsByRoot?: ReadonlyMap<number, ThreadSummary>;
  readonly onOpenThread?: (rootSeq: number) => void;
  // A reply's quote jumps to the message it answers.
  readonly onJumpTo?: (seq: number) => void;
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
  return item.seq ?? null;
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
      {extras.onOpenThread && !extras.threadsByRoot?.has(seq) && (
        <button
          type="button"
          data-reply-in-thread
          onClick={() => extras.onOpenThread?.(seq)}
          title="Reply in thread"
          aria-label="Reply in thread"
          className={iconButton}
        >
          <ChatsCircleIcon size={12} />
        </button>
      )}
      <FindAWayButton item={item} />
    </span>
  );
}

// ThreadLine is a thread's line under its root: how many replies, who
// wrote, closed or not; it opens the thread.
export function ThreadLine({ item, className }: { item: FeedItem; className?: string }) {
  const extras = useContext(MessageExtrasContext);
  const seq = persistedSeq(item);
  const thread = seq == null ? undefined : extras?.threadsByRoot?.get(seq);
  if (!thread || !extras?.onOpenThread || seq == null) return null;
  return (
    <button
      type="button"
      data-thread-line={thread.threadId}
      onClick={() => extras.onOpenThread?.(seq)}
      className={cn(
        "tactile flex items-center gap-1.5 rounded-[var(--radius)] px-1.5 py-0.5 text-xs font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent-soft)]",
        className,
      )}
      title={`Open the thread "${thread.title}"`}
    >
      <ChatsCircleIcon size={14} />
      <span>{rootLine(thread)}</span>
    </button>
  );
}

// ReplyQuote is the message a reply answers, as it read then (§2.3); it
// jumps to it when the feed holds it.
export function ReplyQuote({ item, className }: { item: FeedItem; className?: string }) {
  const extras = useContext(MessageExtrasContext);
  const author = useDisplayName(item.replyTo?.author ?? "");
  const ref = item.replyTo;
  if (!ref) return null;
  return (
    <button
      type="button"
      data-reply-quote={ref.seq}
      onClick={() => extras?.onJumpTo?.(ref.seq)}
      disabled={!extras?.onJumpTo}
      className={cn(
        "tactile flex max-w-full items-start gap-1.5 border-l-2 border-[var(--color-border-strong)] pl-2 text-left text-xs text-[var(--color-fg-subtle)] enabled:hover:text-[var(--color-fg)]",
        className,
      )}
      title="The message this answers"
    >
      <ArrowBendUpLeftIcon size={12} className="mt-0.5 shrink-0" />
      <span className="min-w-0 truncate">
        {ref.author && <span className="font-medium">{author || ref.author}: </span>}
        {ref.excerpt ?? `message ${ref.seq}`}
      </span>
    </button>
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
