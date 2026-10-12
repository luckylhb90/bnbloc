import { useCallback, useEffect, useLayoutEffect, useRef, useState, type CSSProperties } from "react";
import { ArrowSquareOutIcon, EyeSlashIcon, UsersIcon, XIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import type { LocalAgent } from "@/lib/useLocalAgent";
import { startPointerDrag } from "@/lib/pointerDrag";
import type { RoomAccessEnd } from "@/lib/roomGone";
import { useRoomStream } from "@/lib/useRoomStream";
import { ConfirmDialog } from "@/ui/Dialog";
import { MessageFeed } from "@/views/room/MessageFeed";
import { RoomComposer } from "@/views/room/RoomComposer";
import type { FeedItem } from "@/views/room/types";
import { useFeedScroll } from "@/views/room/useFeedScroll";
import { useMessageJump } from "@/views/room/useMessageJump";
import { useRoomFeed } from "@/views/room/useRoomFeed";
import { useRoomMessageActions } from "@/views/room/useRoomMessageActions";

const NO_WORKING = new Map<string, string>();
const NO_JOBS = {};
const NO_ARTIFACTS = {};
const noop = () => {};

// PeopleChatPanel is the room's people chat: a human-only side conversation
// agents never see (the server withholds it from every agent stream, from the
// agent's room context and from plugin hooks). It reuses the room's feed,
// history, stream and send machinery on the people-chat channel, with a
// people-only composer; "Ask the agent about this" is the one explicit bridge
// from here to the agent (a quoted reference in the main composer). It runs
// its own channel-scoped stream, so the same component serves the docked
// panel, the mobile sheet and the desktop pop-out window.
export function PeopleChatPanel({
  roomId,
  channelId,
  subject,
  joinedGen,
  agent,
  members,
  online,
  jumpSeq,
  onJumped,
  onAskAgent,
  onClose,
  onPopOut,
  onGone,
}: {
  readonly roomId: string;
  readonly channelId: string;
  readonly subject: string | null;
  readonly joinedGen: number;
  readonly agent: LocalAgent;
  readonly members: string[];
  readonly online: string[];
  // Scroll to this message once loaded (a deep link / search hit / task).
  readonly jumpSeq: number | null;
  readonly onJumped: () => void;
  readonly onAskAgent: (item: FeedItem) => void;
  readonly onClose?: () => void;
  // Desktop shell only: open the people chat in its own window.
  readonly onPopOut?: () => void;
  readonly onGone: (reason: RoomAccessEnd) => void;
}) {
  const scroller = useFeedScroll();
  const conversation = useRoomFeed({
    roomId,
    channelId,
    subject,
    joinedGen,
    beforePrepend: scroller.beforePrepend,
    onStaleChannel: onClose ?? noop,
  });
  const { feed, setFeed } = conversation;
  useLayoutEffect(scroller.settle, [feed, scroller.settle]);

  const stream = useRoomStream({
    roomId,
    subject,
    joinedGen,
    channelId,
    feed,
    historyLoaded: conversation.history.loaded,
    onMessageEvent: (e) => void conversation.applyEvent(e),
    onReconnect: noop,
    onRevoked: onGone,
    scope: { channelOnly: true },
  });

  const { send, retry, deleteOwnMessage, editOwnMessage } = useRoomMessageActions({
    roomId,
    channelId,
    subject,
    setFeed,
    setRoom: noop,
    onStaleChannel: onClose ?? noop,
  });
  const [confirmDelete, setConfirmDelete] = useState<FeedItem | null>(null);
  const requestDelete = useCallback(
    (item: FeedItem) => {
      if (item.seq == null) void deleteOwnMessage(item);
      else setConfirmDelete(item);
    },
    [deleteOwnMessage],
  );

  const jump = useMessageJump({
    roomId,
    channelId,
    feed,
    history: conversation.history,
    loadEarlier: conversation.loadEarlier,
    selectChannel: noop,
    fromQueue: false,
  });
  useEffect(() => {
    if (jumpSeq == null) return;
    jump({ seq: jumpSeq, channelId });
    onJumped();
    // jump is a fresh closure each render; the request is keyed by jumpSeq.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jumpSeq]);

  return (
    <section aria-label="People chat" className="@container flex h-full min-h-0 flex-col bg-[var(--color-surface)]">
      <header className="pad-safe pad-safe-t flex items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 [--pad-safe-x:0.75rem] [--pad-safe-y:0.5rem]">
        <UsersIcon size={18} className="shrink-0 text-[var(--color-accent)]" />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-semibold">People chat</h2>
          <p className="flex items-center gap-1 truncate text-xs text-[var(--color-fg-subtle)]">
            <EyeSlashIcon size={12} className="shrink-0" aria-hidden />
            Agents can't see this conversation
          </p>
        </div>
        {onPopOut && (
          <button
            type="button"
            onClick={onPopOut}
            className="tap tactile grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
            title="Pop out into its own window"
            aria-label="Pop out the people chat into its own window"
          >
            <ArrowSquareOutIcon size={16} />
          </button>
        )}
        {onClose && (
          <button
            type="button"
            data-layer-close
            onClick={onClose}
            className="tap tactile grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
            title="Close the people chat"
            aria-label="Close the people chat"
          >
            <XIcon size={18} />
          </button>
        )}
      </header>
      <div ref={scroller.scrollRef} onScroll={scroller.onScroll} className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
        <div ref={scroller.contentRef}>
          <MessageFeed
            loaded={conversation.history.loaded}
            roomId={roomId}
            feed={feed}
            visibleFeed={feed}
            working={NO_WORKING}
            query=""
            search=""
            agentTarget=""
            agentIsRemoteGateway={false}
            starting={false}
            hasMoreHistory={conversation.history.hasMore}
            loadingHistory={conversation.history.loading}
            unreadFromSeq={stream.unreadFromSeq}
            showDesktopHandoff={false}
            agentJobs={NO_JOBS}
            artifactsBySeq={NO_ARTIFACTS}
            onStartAgent={noop}
            onLoadEarlier={conversation.loadEarlier}
            onRetry={retry}
            onDelete={requestDelete}
            onEdit={editOwnMessage}
            onCancelJob={noop}
            onOpenArtifactInFiles={noop}
            people
            onAskAgent={onAskAgent}
          />
        </div>
      </div>
      <RoomComposer
        variant="people"
        roomPreview={false}
        agent={agent}
        subject={subject}
        roomId={roomId}
        channelId={channelId}
        typingUsers={stream.typingUsers}
        roomAgents={[]}
        roomMembers={members}
        online={online}
        feed={feed}
        joining={false}
        onSend={send}
        onJoin={noop}
      />
      <ConfirmDialog
        open={confirmDelete !== null}
        onClose={() => setConfirmDelete(null)}
        onConfirm={() => {
          if (confirmDelete) void deleteOwnMessage(confirmDelete);
        }}
        title="Delete this message?"
        description="It will be removed for everyone in the people chat. This cannot be undone."
        confirmLabel="Delete"
        danger
      />
    </section>
  );
}

const WIDTH_KEY = "bnbloc.peopleChatWidth";
const DEFAULT_WIDTH = 360;
const MIN_WIDTH = 280;
const MAX_WIDTH = 720;

export function clampPanelWidth(px: number): number {
  if (!Number.isFinite(px)) return DEFAULT_WIDTH;
  return Math.round(Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, px)));
}

function storedWidth(): number {
  const saved = Number(localStorage.getItem(WIDTH_KEY));
  return saved > 0 ? clampPanelWidth(saved) : DEFAULT_WIDTH;
}

// PeopleChatDock is the wide-screen presentation: a column docked beside the
// agent timeline, resized by its drag handle (width persisted per browser).
// The column never takes more than half the row, whatever the stored width.
export function PeopleChatDock({ children }: { readonly children: React.ReactNode }) {
  const [width, setWidth] = useState(storedWidth);
  const [dragging, setDragging] = useState(false);
  const asideRef = useRef<HTMLElement | null>(null);
  const startDrag = (e: React.PointerEvent) => {
    const right = asideRef.current?.getBoundingClientRect().right;
    if (right == null) return;
    setDragging(true);
    let last = -1;
    startPointerDrag(
      e,
      (ev) => {
        const next = clampPanelWidth(right - ev.clientX);
        if (next !== last) {
          last = next;
          setWidth(next);
        }
      },
      () => {
        setDragging(false);
        if (last > 0) localStorage.setItem(WIDTH_KEY, String(last));
      },
    );
  };
  const style: CSSProperties = { width: `min(${width}px, 50%)` };
  return (
    <>
      <div
        onPointerDown={startDrag}
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize the people chat"
        className="group relative z-10 w-1.5 shrink-0 cursor-col-resize"
        title="Drag to resize the people chat"
      >
        <div className="absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-[var(--color-border)] transition-colors group-hover:bg-[var(--color-accent)] group-active:bg-[var(--color-accent)]" />
      </div>
      <aside
        ref={asideRef}
        data-people-chat-dock
        className={cn("flex min-w-0 shrink-0 flex-col overflow-hidden", dragging && "select-none")}
        style={style}
      >
        {children}
      </aside>
    </>
  );
}
