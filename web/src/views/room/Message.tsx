import { memo, useEffect, useState } from "react";
import {
  FlagIcon,
  HourglassIcon,
  RobotIcon,
  CopyIcon,
  CheckIcon,
  HandshakeIcon,
  WarningCircleIcon,
  ArrowClockwiseIcon,
  PencilSimpleIcon,
  TrashIcon,
  ClockIcon,
  UsersIcon,
  ArrowBendUpRightIcon,
  ArrowsSplitIcon,
} from "@phosphor-icons/react";
import { relativeTime, exactTime } from "@/lib/format";
import type { FeedItem } from "@/views/room/types";
import { MessageAttachments } from "@/views/room/MessageAttachments";
import { ActorLabel } from "@/views/room/ActorLabel";
import { UserName } from "@/views/UserName";
import { useDisplayName } from "@/lib/useDisplayName";
import { MessageBody } from "@/views/room/MessageBody";
import { ThinkingDisclosure } from "@/views/room/ThinkingDisclosure";
import { TurnMetricsFooter } from "@/views/room/TurnMetricsFooter";
import { MessageExtraActions, TaskBadge } from "@/views/room/messageExtras";
import { dispatchLine, type DispatchAttribution } from "@/lib/dispatch";
import { ExpertTag } from "@/views/room/ExpertTag";
import { FileRefTag } from "@/views/room/FileRefTag";
import { isSubmitEnter } from "@/lib/keys";
import { copyText } from "@/lib/clipboard";

// sameSender groups consecutive messages from the same author on the same side,
// so a run of agent replies stacks without repeating the avatar and name.
export function sameSender(a: FeedItem, b: FeedItem): boolean {
  return a.self === b.self && a.author === b.author && a.agentLabel === b.agentLabel;
}

export function Avatar({ label, agent }: { label: string; agent?: boolean }) {
  // A person's initial comes from their display name, never the raw id.
  const name = useDisplayName(agent ? null : label);
  const initial = (agent ? label : name || label)[0] || "?";
  return (
    <div
      className={
        agent
          ? "grid size-7 shrink-0 place-items-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)]"
          : "grid size-7 shrink-0 place-items-center rounded-full bg-[var(--color-surface-2)] text-[var(--color-fg-muted)]"
      }
    >
      {agent ? (
        <RobotIcon size={15} weight="fill" />
      ) : (
        <span className="text-xs font-semibold">{initial.toUpperCase()}</span>
      )}
    </div>
  );
}

// CopyButton copies a message's text to the clipboard, flashing a check on
// success. A tiny, unobtrusive control shown in the message meta row.
export function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await copyText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      /* clipboard unavailable; ignore */
    }
  };
  return (
    <button
      onClick={copy}
      title="Copy message"
      className="tactile flex items-center gap-1 text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
    >
      {copied ? <CheckIcon size={12} /> : <CopyIcon size={12} />}
    </button>
  );
}

// PeopleOnlyMarker flags a member's message addressed to people only: the
// agent reads it as room context but no reply is coming.
function PeopleOnlyMarker() {
  return (
    <span
      data-people-only
      title="No agent reply requested"
      aria-label="No agent reply requested"
      role="img"
      className="inline-flex items-center text-[var(--color-fg-subtle)]"
    >
      <UsersIcon size={12} />
    </span>
  );
}

// DispatchedVia attributes a message an agent dispatched on its author's
// behalf: "maya → via jack/codex → bob/claude". The author is the person
// whose request it serves (and whose authority it runs under).
function DispatchedVia({ author, dispatch, className }: { author: string; dispatch: DispatchAttribution; className?: string }) {
  const line = dispatchLine(author, dispatch);
  return (
    <span
      data-dispatched
      title={`Sent by ${dispatch.via} on behalf of ${author} (step ${dispatch.depth} of a delegated request)`}
      aria-label={line}
      className={"inline-flex min-w-0 items-center gap-1 text-[0.68rem] text-[var(--color-fg-subtle)] " + (className ?? "")}
    >
      <ArrowsSplitIcon size={12} className="shrink-0" />
      <span className="truncate">
        <UserName id={author} /> → via {dispatch.via}
        {dispatch.to ? <> → {dispatch.to}</> : null}
        {dispatch.role ? (
          <span data-team-role className="ml-1 rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] px-1.5 text-[var(--color-accent)]">
            {dispatch.role}
          </span>
        ) : null}
        {dispatch.expert ? <> (as {dispatch.expert})</> : null}
      </span>
    </span>
  );
}

// AskAgentButton (people chat only) carries a message into the room's main
// composer as a quoted reference — the explicit, member-driven bridge from
// the private conversation to the agent.
function AskAgentButton({ item, onAskAgent, className }: { item: FeedItem; onAskAgent: (item: FeedItem) => void; className?: string }) {
  return (
    <button
      type="button"
      onClick={() => onAskAgent(item)}
      title="Ask the agent about this"
      aria-label="Ask the agent about this"
      className={
        "tactile inline-flex items-center gap-1 rounded-sm text-[var(--color-fg-subtle)] hover:text-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60 " +
        (className ?? "")
      }
    >
      <ArrowBendUpRightIcon size={12} />
      <span className="hover-reveal hidden text-[0.68rem]">Ask the agent</span>
    </button>
  );
}

// useReveal animates a live agent reply in a few characters per frame, as
// LOCAL state of the one message: the core delivers replies whole, so this is
// presentation only, and it must not churn the room's feed state (which
// re-rendered the whole room every 16 ms).
function useReveal(text: string, animate: boolean | undefined): string {
  const [shown, setShown] = useState(() => (animate ? 0 : text.length));
  useEffect(() => {
    if (shown >= text.length) return;
    const step = Math.max(2, Math.ceil(text.length / 60));
    const id = window.requestAnimationFrame(() => setShown((n) => Math.min(text.length, n + step)));
    return () => window.cancelAnimationFrame(id);
  }, [shown, text.length]);
  return shown >= text.length ? text : text.slice(0, shown);
}

// Message is memoized: rows re-render only when their own item (or grouping)
// changes, not on every room update. Callers pass stable callbacks.
export const Message = memo(function Message({
  roomId,
  item,
  grouped,
  onRetry,
  onDelete,
  onEdit,
  onAskAgent,
  showPeopleOnly = true,
}: {
  roomId: string;
  item: FeedItem;
  grouped?: boolean;
  onRetry?: (item: FeedItem) => void;
  onDelete?: (item: FeedItem) => void;
  onEdit?: (item: FeedItem, text: string) => void;
  // People chat: offer "Ask the agent about this" on persisted messages.
  onAskAgent?: (item: FeedItem) => void;
  // Mark people-only messages (off in the people chat, where every message
  // is people-only).
  showPeopleOnly?: boolean;
}) {
  const peopleMarker = showPeopleOnly && item.peopleOnly;
  const canAsk = !!onAskAgent && !!item.text && item.status !== "pending" && item.status !== "failed";
  const isAgent = item.author === "agent";
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(item.text);
  const shown = useReveal(item.text, item.animate);

  // An agent's notice line (a team lead's progress, a member that has not
  // finished): one compact line under the agent's name, not a reply.
  if (isAgent && item.notice) {
    const line = (
      <div className="flex min-w-0 items-center gap-1.5">
        {item.notice === "member-timeout" ? <HourglassIcon size={12} className="shrink-0" /> : <FlagIcon size={12} className="shrink-0" />}
        {/* By display name like every other agent row: never the raw
            owner id (privacy mode, web M15). */}
        <ActorLabel actor={item.agentLabel || "Agent"} className="shrink-0" />
        <span className="min-w-0 break-words">{item.text}</span>
        {item.createdAtMs ? (
          <span className="shrink-0 text-[0.68rem] text-[var(--color-fg-subtle)]" title={exactTime(item.createdAtMs)}>
            {relativeTime(item.createdAtMs)}
          </span>
        ) : null}
      </div>
    );
    return (
      <div id={`msg-${item.id}`} data-notice={item.notice} className="ml-10 mt-1 flex min-w-0 flex-col gap-1 text-xs text-[var(--color-fg-muted)]">
        {line}
        {/* The agent's own error output: fenced and verbatim, never
            Markdown and never a reply. */}
        {item.noticeDetail ? (
          <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-2 py-1 font-mono text-[0.7rem] [overflow-wrap:anywhere]">
            {item.noticeDetail}
          </pre>
        ) : null}
      </div>
    );
  }

  // Own messages stay as a compact right-aligned bubble (short user turns read
  // well that way). Everyone else — agent and other members — renders flat and
  // full-width with an avatar gutter, like a document, so long Markdown and code
  // are readable instead of crammed into a centered card.
  if (item.self) {
    const failed = item.status === "failed";
    const pending = item.status === "pending";
    // Imported history rows have no author on the server: never editable.
    const canEdit = !!onEdit && item.seq != null && !item.importedFrom;
    const submitEdit = () => {
      const next = draft.trim();
      setEditing(false);
      if (next && next !== item.text) onEdit?.(item, next);
    };
    return (
      <div id={`msg-${item.id}`} className="flex flex-col items-end scroll-mt-4">
        {editing ? (
          <div className="flex w-full max-w-[80%] flex-col items-end gap-1.5">
            <textarea
              autoFocus
              rows={Math.min(6, draft.split("\n").length)}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  e.preventDefault();
                  setDraft(item.text);
                  setEditing(false);
                } else if (isSubmitEnter(e)) {
                  e.preventDefault();
                  submitEdit();
                }
              }}
              aria-label="Edit message"
              className="w-full resize-none rounded-2xl rounded-br-md border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-4 py-2.5 text-sm text-[var(--color-fg)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
            />
            <div className="flex items-center gap-2 px-1 text-[0.68rem]">
              <button
                onClick={submitEdit}
                className="tactile font-medium text-[var(--color-accent)] hover:opacity-80"
              >
                Save
              </button>
              <button
                onClick={() => {
                  setDraft(item.text);
                  setEditing(false);
                }}
                className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
              >
                Cancel
              </button>
              <span className="text-[var(--color-fg-subtle)]">
                Enter to save · Esc to cancel
              </span>
            </div>
          </div>
        ) : (
          <div className="flex max-w-[80%] min-w-0 flex-col items-end gap-2">
            {item.text && (
              <div
                className={
                  "min-w-0 rounded-2xl rounded-br-md px-4 py-2.5 text-sm whitespace-pre-wrap break-words leading-relaxed animate-in transition-shadow duration-500 data-[flash=true]:ring-2 data-[flash=true]:ring-[var(--color-accent)]/60 [overflow-wrap:anywhere] " +
                  (failed
                    ? "bg-[var(--color-danger)]/20 text-[var(--color-fg)]"
                    : "bg-[var(--color-accent)] text-[var(--color-accent-fg)]") +
                  (pending ? " opacity-70" : "")
                }
              >
                {item.text}
              </div>
            )}
            <MessageAttachments roomId={roomId} attachments={item.attachments} />
            <FileRefTag roomId={roomId} fileRef={item.fileRef} />
          </div>
        )}
        {!editing && (
          <div className="mt-0.5 flex items-center gap-1.5 px-1 text-[0.68rem] text-[var(--color-fg-subtle)]">
            {item.borrowedFrom && (
              <span className="flex items-center gap-1 rounded-full bg-[var(--color-accent-soft)] px-1.5 text-[var(--color-accent)]">
                <HandshakeIcon size={10} weight="fill" />
                Borrowed from <UserName id={item.borrowedFrom} />
              </span>
            )}
            {failed ? (
              <>
                <button
                  onClick={() => onRetry?.(item)}
                  className="tactile flex items-center gap-1 text-[var(--color-danger)] hover:opacity-80"
                >
                  <WarningCircleIcon size={12} weight="fill" />
                  Failed — retry
                  <ArrowClockwiseIcon size={12} />
                </button>
                {onDelete && item.seq == null && (
                  <button
                    onClick={() => onDelete(item)}
                    title="Discard this unsent message"
                    aria-label="Discard unsent message"
                    className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
                  >
                    Discard
                  </button>
                )}
              </>
            ) : pending ? (
              <span>Sending…</span>
            ) : (
              <>
                <TaskBadge item={item} />
                {peopleMarker && <PeopleOnlyMarker />}
                <span title={exactTime(item.createdAtMs)}>
                  {relativeTime(item.createdAtMs)}
                </span>
                {item.editedAtMs != null && (
                  <span title={`Edited ${exactTime(item.editedAtMs)}`}>(edited)</span>
                )}
                {item.importedFrom && (
                  <span
                    data-imported
                    title={`Imported from ${item.importedFrom}`}
                    className="rounded-full border border-[var(--color-border)] px-1.5 text-[10px] uppercase tracking-wide"
                  >
                    imported
                  </span>
                )}
                {item.scheduledId && (
                  <span data-scheduled title="Scheduled message" aria-label="Scheduled message" className="inline-flex items-center">
                    <ClockIcon size={12} />
                  </span>
                )}
                {item.dispatch && <DispatchedVia author={item.author} dispatch={item.dispatch} />}
                {item.expert && <ExpertTag expert={item.expert} />}
                <CopyButton text={item.text} />
                {canAsk && onAskAgent && <AskAgentButton item={item} onAskAgent={onAskAgent} />}
                <MessageExtraActions item={item} />
                {canEdit && (
                  <button
                    onClick={() => {
                      setDraft(item.text);
                      setEditing(true);
                    }}
                    title="Edit message"
                    aria-label="Edit message"
                    className="tactile grid size-11 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
                  >
                    <PencilSimpleIcon size={12} />
                  </button>
                )}
                {onDelete && item.seq != null && !item.importedFrom && (
                  <button
                    onClick={() => onDelete(item)}
                    title="Delete message"
                    aria-label="Delete message"
                    className="tactile grid size-11 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
                  >
                    <TrashIcon size={12} />
                  </button>
                )}
              </>
            )}
          </div>
        )}
      </div>
    );
  }

  return (
    <div className={grouped ? "group relative flex gap-3" : "mt-3 flex gap-3"}>
      {grouped && <MessageExtraActions item={item} floating />}
      {grouped && canAsk && onAskAgent && (
        <span className="absolute right-0 top-0 z-[1] rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-1.5 py-1 opacity-0 shadow-[var(--shadow-pop)] transition-opacity group-hover:opacity-100 focus-within:opacity-100 [@media(hover:none)]:opacity-100">
          <AskAgentButton item={item} onAskAgent={onAskAgent} />
        </span>
      )}
      {grouped ? (
        <div className="flex w-7 shrink-0 justify-center pt-1">{peopleMarker && <PeopleOnlyMarker />}</div>
      ) : (
        <Avatar label={item.author} agent={isAgent} />
      )}
      <div className="min-w-0 flex-1 animate-in">
        {!grouped && (
          <div className="mb-1 flex items-center gap-2">
            <ActorLabel actor={isAgent ? item.agentLabel || "Agent" : item.author} />
            {item.createdAtMs ? (
              <span
                className="text-[0.68rem] text-[var(--color-fg-subtle)]"
                title={exactTime(item.createdAtMs)}
              >
                {relativeTime(item.createdAtMs)}
              </span>
            ) : null}
            {peopleMarker && <PeopleOnlyMarker />}
            <CopyButton text={item.text} />
            {canAsk && onAskAgent && <AskAgentButton item={item} onAskAgent={onAskAgent} />}
            <MessageExtraActions item={item} />
            <TaskBadge item={item} />
          </div>
        )}
        {grouped && <TaskBadge item={item} className="mb-1" />}
        {item.dispatch && <DispatchedVia author={item.author} dispatch={item.dispatch} className="mb-1" />}
        {item.expert && !item.self && <ExpertTag expert={item.expert} className="mb-1" />}
        {isAgent && item.thinking ? (
          <ThinkingDisclosure thinking={item.thinking} metrics={item.metrics} />
        ) : null}
        <MessageBody text={shown} markdown={isAgent} />
        {isAgent && item.metrics ? <TurnMetricsFooter metrics={item.metrics} /> : null}
        <MessageAttachments roomId={roomId} attachments={item.attachments} className="mt-2" />
        <FileRefTag roomId={roomId} fileRef={item.fileRef} className="mt-1" />
      </div>
    </div>
  );
});
