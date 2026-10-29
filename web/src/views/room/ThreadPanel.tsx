import { useCallback, useLayoutEffect, useState } from "react";
import { ArchiveIcon, BellIcon, BellSlashIcon, ChatsCircleIcon, PencilSimpleIcon, XIcon } from "@phosphor-icons/react";
import { channelsApi } from "@/lib/channelsApi";
import { errorText } from "@/lib/errors";
import type { LocalAgent } from "@/lib/useLocalAgent";
import type { RoomAccessEnd } from "@/lib/roomGone";
import { rootLine, summaryOf } from "@/lib/threads";
import { useRoomStream } from "@/lib/useRoomStream";
import type { RoomChannel } from "@/lib/wire.gen";
import { ConfirmDialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";
import { Message } from "@/views/room/Message";
import { MessageFeed } from "@/views/room/MessageFeed";
import { RoomComposer } from "@/views/room/RoomComposer";
import type { FeedItem } from "@/views/room/types";
import { useFeedScroll } from "@/views/room/useFeedScroll";
import { useRoomFeed } from "@/views/room/useRoomFeed";
import { useRoomMessageActions } from "@/views/room/useRoomMessageActions";
import type { RoomAgent } from "@/views/composer/types";

const NO_WORKING = new Map<string, string>();
const NO_JOBS = {};
const NO_ARTIFACTS = {};
const noop = () => {};

const headerButton =
  "tap tactile grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60";

// ThreadPanel is one thread in the side panel (docs/prd/2026-10-28-channels-
// and-threads.md §2.2): its own conversation, on the room's feed, history,
// stream and send machinery scoped to the thread's channel (a thread is a
// channel, C1), so the agent answering here has a session of its own. The
// header renames it, follows or unfollows it and closes it (C3: archived,
// read-only). Every message posted here answers the root by default.
export function ThreadPanel({
  roomId,
  thread,
  subject,
  joinedGen,
  agent,
  roomAgents,
  members,
  online,
  mayPost,
  root,
  onChanged,
  onClose,
  onGone,
}: {
  readonly roomId: string;
  readonly thread: RoomChannel;
  readonly subject: string | null;
  readonly joinedGen: number;
  readonly agent: LocalAgent;
  readonly roomAgents: RoomAgent[];
  readonly members: string[];
  readonly online: string[];
  // The member may post in the parent channel (and so here): rename,
  // archive and the composer depend on it.
  readonly mayPost: boolean;
  // The root message as the parent channel's feed holds it (undefined when
  // that feed does not have it loaded): shown above the replies, so the
  // thread reads as it was asked.
  readonly root?: FeedItem;
  // The thread's title, following or archive changed (the room reloads).
  readonly onChanged: () => void;
  readonly onClose: () => void;
  readonly onGone: (reason: RoomAccessEnd) => void;
}) {
  const toast = useToast();
  const channelId = thread.channelId;
  const scroller = useFeedScroll();
  const conversation = useRoomFeed({ roomId, channelId, subject, joinedGen, beforePrepend: scroller.beforePrepend, onStaleChannel: onClose });
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
  const { send, retry, deleteOwnMessage, editOwnMessage } = useRoomMessageActions({ roomId, channelId, subject, setFeed, setRoom: noop, onStaleChannel: onClose });
  const [confirmDelete, setConfirmDelete] = useState<FeedItem | null>(null);
  const requestDelete = useCallback((item: FeedItem) => (item.seq == null ? void deleteOwnMessage(item) : setConfirmDelete(item)), [deleteOwnMessage]);

  const [title, setTitle] = useState<string | null>(null);
  const [confirmArchive, setConfirmArchive] = useState(false);
  const archived = (thread.archivedAtMs ?? 0) > 0;
  const summary = summaryOf(thread);

  const rename = () => {
    const next = (title ?? "").trim();
    setTitle(null);
    if (!next || next === thread.name) return;
    channelsApi.patch(roomId, channelId, { name: next }).then(onChanged, (e: unknown) => toast.error(errorText(e, "Could not rename the thread")));
  };
  const toggleFollow = () => {
    const call = thread.joined ? channelsApi.unfollow(roomId, channelId) : channelsApi.follow(roomId, channelId);
    call.then(onChanged, (e: unknown) => toast.error(errorText(e, "Could not change following")));
  };
  const archive = () =>
    channelsApi.archive(roomId, channelId, true).then(onChanged, (e: unknown) => toast.error(errorText(e, "Could not close the thread")));

  return (
    <section aria-label={`Thread: ${thread.name}`} data-thread-panel={channelId} className="@container flex h-full min-h-0 flex-col bg-[var(--color-surface)]">
      <header className="pad-safe pad-safe-t flex items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 [--pad-safe-x:0.75rem] [--pad-safe-y:0.5rem]">
        <ChatsCircleIcon size={18} className="shrink-0 text-[var(--color-accent)]" />
        <div className="min-w-0 flex-1">
          {title !== null ? (
            <input
              autoFocus
              aria-label="Thread title"
              value={title}
              maxLength={120}
              onChange={(e) => setTitle(e.target.value)}
              onBlur={rename}
              onKeyDown={(e) => {
                if (e.key === "Enter") rename();
                if (e.key === "Escape") setTitle(null);
              }}
              className="w-full rounded-[var(--radius)] bg-[var(--color-surface-2)] px-2 py-1 text-sm font-semibold outline-none"
            />
          ) : (
            <h2 className="truncate text-sm font-semibold">{thread.name}</h2>
          )}
          <p className="truncate text-xs text-[var(--color-fg-subtle)]">{summary ? rootLine(summary) : "Thread"}</p>
        </div>
        {mayPost && !archived && title === null && (
          <button type="button" onClick={() => setTitle(thread.name)} className={headerButton} title="Rename the thread" aria-label="Rename the thread">
            <PencilSimpleIcon size={16} />
          </button>
        )}
        <button
          type="button"
          data-thread-follow
          onClick={toggleFollow}
          className={headerButton}
          title={thread.joined ? "Unfollow: no unread or notifications from this thread" : "Follow: its replies show as unread"}
          aria-label={thread.joined ? "Unfollow the thread" : "Follow the thread"}
          aria-pressed={!!thread.joined}
        >
          {thread.joined ? <BellIcon size={16} weight="fill" /> : <BellSlashIcon size={16} />}
        </button>
        {mayPost && !archived && (
          <button type="button" data-thread-archive onClick={() => setConfirmArchive(true)} className={headerButton} title="Close the thread (read-only)" aria-label="Close the thread">
            <ArchiveIcon size={16} />
          </button>
        )}
        <button type="button" data-layer-close onClick={onClose} className={headerButton} title="Close the panel" aria-label="Close the thread panel">
          <XIcon size={18} />
        </button>
      </header>
      <div ref={scroller.scrollRef} onScroll={scroller.onScroll} className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
        <div ref={scroller.contentRef}>
          {root && (
            <div data-thread-root className="mb-3 border-b border-[var(--color-border)] pb-3">
              <Message roomId={roomId} item={root} />
            </div>
          )}
          <MessageFeed
            loaded={conversation.history.loaded}
            roomId={roomId}
            feed={feed}
            visibleFeed={feed}
            working={NO_WORKING}
            query=""
            search=""
            agentTarget={agent.target}
            agentIsRemoteGateway={agent.isRemoteGateway}
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
          />
        </div>
      </div>
      <RoomComposer
        readOnly={archived || !mayPost}
        agent={agent}
        subject={subject}
        roomId={roomId}
        channelId={channelId}
        typingUsers={stream.typingUsers}
        roomAgents={roomAgents}
        roomMembers={members}
        online={online}
        feed={feed}
        onSend={send}
      />
      <ConfirmDialog
        open={confirmDelete !== null}
        onClose={() => setConfirmDelete(null)}
        onConfirm={() => {
          if (confirmDelete) void deleteOwnMessage(confirmDelete);
        }}
        title="Delete this message?"
        description="It will be removed for everyone in the thread. This cannot be undone."
        confirmLabel="Delete"
        danger
      />
      <ConfirmDialog
        open={confirmArchive}
        onClose={() => setConfirmArchive(false)}
        onConfirm={() => void archive()}
        title="Close this thread?"
        description="It becomes read-only for everyone. An owner can restore it."
        confirmLabel="Close thread"
      />
    </section>
  );
}
