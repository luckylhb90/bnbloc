import { useEffect, useState } from "react";
import { CaretDownIcon, CaretUpIcon, CheckSquareIcon, ListChecksIcon, SquareIcon } from "@phosphor-icons/react";
import type { MessageTask, TaskStatus } from "@/lib/api";
import { cn } from "@/lib/cn";
import { ActorName, UserName } from "@/views/UserName";

// RoomTasksStrip is the room's task summary (F6): a compact pill floating at
// the top-right of the conversation ("3 open tasks") that expands into the
// task list — open tasks with a done checkbox and jump-to-message, done tasks
// collapsed below. It is absolutely positioned over the feed, so it never
// shifts the feed layout or its scroll position. Hidden while the room has no
// tasks.
export function RoomTasksStrip({
  tasks,
  onSetStatus,
  onJump,
}: {
  readonly tasks: readonly MessageTask[];
  readonly onSetStatus: (seq: number, status: TaskStatus) => void;
  readonly onJump: (task: MessageTask) => void;
}) {
  const [open, setOpen] = useState(false);
  const [showDone, setShowDone] = useState(false);
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);
  if (tasks.length === 0) return null;
  const openTasks = tasks.filter((t) => t.status !== "done");
  const doneTasks = tasks.filter((t) => t.status === "done");
  const label =
    openTasks.length === 0
      ? `All ${doneTasks.length} tasks done`
      : `${openTasks.length} open task${openTasks.length === 1 ? "" : "s"}`;

  const row = (t: MessageTask) => {
    const done = t.status === "done";
    return (
      <li key={t.seq} className="flex items-start gap-2 px-3 py-2 hover:bg-[var(--color-surface-2)]">
        <button
          type="button"
          onClick={() => onSetStatus(t.seq, done ? "open" : "done")}
          title={done ? "Mark as open" : "Mark as done"}
          aria-label={done ? `Mark “${t.title}” as open` : `Mark “${t.title}” as done`}
          className="tactile mt-0.5 shrink-0 text-[var(--color-fg-subtle)] hover:text-[var(--color-accent)]"
        >
          {done ? <CheckSquareIcon size={16} weight="fill" className="text-[var(--color-accent)]" /> : <SquareIcon size={16} />}
        </button>
        <button
          type="button"
          onClick={() => {
            onJump(t);
            setOpen(false);
          }}
          title="Jump to message"
          className="tactile min-w-0 flex-1 text-left"
        >
          <span className={cn("block truncate text-sm", done ? "text-[var(--color-fg-subtle)] line-through" : "text-[var(--color-fg)]")}>
            {t.title}
          </span>
          <span className="block truncate text-[0.68rem] text-[var(--color-fg-subtle)]">
            {t.messageAuthor ? <ActorName actor={t.messageAuthor} /> : "unknown"}
            {done && t.doneBy ? (
              <>
                {" "}· done by <UserName id={t.doneBy} />
              </>
            ) : null}
          </span>
        </button>
      </li>
    );
  };

  return (
    <div className="pointer-events-none absolute right-4 top-2 z-10 flex max-w-[calc(100%-2rem)] flex-col items-end @5xl:right-6">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="tactile pointer-events-auto flex items-center gap-1.5 rounded-full border border-[var(--color-border)] bg-[var(--color-surface)]/90 px-2.5 py-1 text-xs text-[var(--color-fg-muted)] shadow-[var(--shadow-pop)] backdrop-blur hover:text-[var(--color-fg)]"
      >
        <ListChecksIcon size={13} className={openTasks.length > 0 ? "text-[var(--color-accent)]" : undefined} />
        {label}
        {open ? <CaretUpIcon size={11} /> : <CaretDownIcon size={11} />}
      </button>
      {open && (
        <div className="pointer-events-auto mt-1.5 w-80 max-w-full overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] shadow-[var(--shadow-diffuse)]">
          <ul className="max-h-72 overflow-y-auto py-1">
            {openTasks.length === 0 && (
              <li className="px-3 py-2 text-xs text-[var(--color-fg-subtle)]">No open tasks.</li>
            )}
            {openTasks.map(row)}
          </ul>
          {doneTasks.length > 0 && (
            <div className="border-t border-[var(--color-border)]">
              <button
                type="button"
                onClick={() => setShowDone((s) => !s)}
                aria-expanded={showDone}
                className="tactile flex w-full items-center justify-between px-3 py-1.5 text-xs text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
              >
                Done ({doneTasks.length})
                {showDone ? <CaretUpIcon size={11} /> : <CaretDownIcon size={11} />}
              </button>
              {showDone && <ul className="max-h-48 overflow-y-auto pb-1">{doneTasks.map(row)}</ul>}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
