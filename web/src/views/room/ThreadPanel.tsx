import { useCallback, useLayoutEffect, useState } from "react";
import { ArchiveIcon, BellIcon, BellSlashIcon, ChatsCircleIcon, PencilSimpleIcon, XIcon } from "@phosphor-icons/react";
import { channelsApi } from "@/lib/channelsApi";
import { errorText } from "@/lib/errors";
import type { LocalAgent } from "@/lib/useLocalAgent";
import type { RoomAccessEnd } from "@/lib/roomGone";
import { AI_UNAVAILABLE, DRAFT_THREAD_TITLE, SUMMARIZE_THREAD } from "@/lib/channelAssists";
import { closingSummary, rootLine, summaryOf, threadAssistText } from "@/lib/threads";
import { isForum } from "@/lib/forum";
import { MaxThreadTags, type ChannelSettings } from "@/lib/wire.gen";
import { Select } from "@/ui/Select";
import { cn } from "@/lib/cn";
import { AssistButton } from "@/views/AssistButton";
import { useChannelAssist } from "@/views/room/useChannelAssist";
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
  parentSettings,
  root,
  onChanged,
  sendToParent,
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
  // The parent channel's settings: a forum's tags and statuses (C20).
  readonly parentSettings?: ChannelSettings;
  // The root message as the parent channel's feed holds it (undefined when
  // that feed does not have it loaded): shown above the replies, so the
  // thread reads as it was asked.
  readonly root?: FeedItem;
  // The thread's title, following or archive changed (the room reloads).
  readonly onChanged: () => void;
  // Post a message to the parent channel answering its root (the closing
  // summary): the room view's send, scoped to the parent.
  readonly sendToParent: (parentId: string, rootSeq: number, text: string) => Promise<void>;
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
    onCatchUp: conversation.catchUp,
    onRevoked: onGone,
    scope: { channelOnly: true },
  });
  const { send, retry, deleteOwnMessage, editOwnMessage } = useRoomMessageActions({ roomId, channelId, subject, setFeed, setRoom: noop, onStaleChannel: onClose });
  const [confirmDelete, setConfirmDelete] = useState<FeedItem | null>(null);
  const requestDelete = useCallback((item: FeedItem) => (item.seq == null ? void deleteOwnMessage(item) : setConfirmDelete(item)), [deleteOwnMessage]);

  const [title, setTitle] = useState<string | null>(null);
  const draftTitle = useChannelAssist(DRAFT_THREAD_TITLE, "suggest a title");
  const summarize = useChannelAssist(SUMMARIZE_THREAD, "summarize the thread");
  // The thread as its assists read it: the root, then the replies.
  const assistText = () =>
    threadAssistText(thread.name, [
      ...(root ? [{ author: root.author, text: root.text }] : []),
      ...feed.filter((m) => m.seq != null).map((m) => ({ author: m.author, text: m.text })),
    ]);
  const suggestTitle = async () => {
    const fields = await draftTitle.run({ text: assistText(), roomId });
    const t = typeof fields?.title === "string" ? fields.title.trim() : "";
    if (t) setTitle(t.slice(0, 120));
  };
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
  // Closing (C3): the AI's summary is posted to the parent as a reply to the
  // root ("decided: …; open: …"), then the thread becomes read-only. With no
  // model it closes without one, and says so.
  const archive = async () => {
    const thread0 = thread.thread;
    if (summarize.available && thread0) {
      const fields = await summarize.run({ text: assistText(), roomId });
      const text = fields ? closingSummary(fields) : "";
      if (text) {
        await sendToParent(thread0.parentId, thread0.rootSeq, `Thread "${thread.name}" closed.\n\n${text}`).catch((e: unknown) =>
          toast.error(errorText(e, "Could not post the thread's summary")),
        );
      }
    } else if (!summarize.available) {
      toast.info(`${AI_UNAVAILABLE} The thread closes without a summary.`);
    }
    await channelsApi.archive(roomId, channelId, true).then(onChanged, (e: unknown) => toast.error(errorText(e, "Could not close the thread")));
  };

  // A forum thread's marks (C20): set by any member who may post.
  const forum = isForum(parentSettings);
  const info = thread.thread;
  const mark = (body: Parameters<typeof channelsApi.markThread>[2]) =>
    channelsApi.markThread(roomId, channelId, body).then(onChanged, (e: unknown) => toast.error(errorText(e, "Could not mark the thread")));
  const replies = feed.filter((m) => m.seq != null && m.text);

  return (
    <section aria-label={`Thread: ${thread.name}`} data-thread-panel={channelId} className="@container flex h-full min-h-0 flex-col bg-[var(--color-surface)]">
      <header className="pad-safe pad-safe-t flex items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 [--pad-safe-x:0.75rem] [--pad-safe-y:0.5rem]">
        <ChatsCircleIcon size={18} className="shrink-0 text-[var(--color-accent)]" />
        <div className="min-w-0 flex-1">
          {title !== null ? (
            <div className="flex items-center gap-1">
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
            {draftTitle.available && (
              <AssistButton
                label="Suggest a title"
                state={draftTitle.state}
                onRun={() => void suggestTitle()}
                className="shrink-0"
              />
            )}
            </div>
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
      {forum && (
        <div data-thread-marks className="flex flex-wrap items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 text-xs">
          {(parentSettings?.statuses?.length ?? 0) > 0 && (
            <Select
              aria-label="Status"
              disabled={!mayPost || archived}
              value={info?.status ?? ""}
              onChange={(e) => void mark({ status: e.target.value })}
              className="h-7 w-auto text-xs"
            >
              <option value="">No status</option>
              {parentSettings!.statuses!.map((st) => (
                <option key={st.id} value={st.id}>
                  {st.name}
                </option>
              ))}
            </Select>
          )}
          {(parentSettings?.tags ?? []).map((t) => {
            const on = (info?.tags ?? []).includes(t.id);
            return (
              <button
                key={t.id}
                type="button"
                aria-pressed={on}
                disabled={!mayPost || archived || (!on && (info?.tags?.length ?? 0) >= MaxThreadTags)}
                onClick={() => void mark({ tags: on ? (info?.tags ?? []).filter((x) => x !== t.id) : [...(info?.tags ?? []), t.id] })}
                className={cn(
                  "tactile rounded-full border px-2 py-0.5",
                  on ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)] text-[var(--color-fg)]" : "border-[var(--color-border)] text-[var(--color-fg-subtle)]",
                )}
              >
                {t.name}
              </button>
            );
          })}
          {replies.length > 0 && (
            <Select
              aria-label="Answer"
              disabled={!mayPost || archived}
              value={String(info?.answeredSeq ?? 0)}
              onChange={(e) => void mark({ answeredSeq: Number(e.target.value) })}
              className="h-7 w-auto max-w-48 text-xs"
            >
              <option value="0">Not answered</option>
              {replies.map((m) => (
                <option key={m.seq} value={String(m.seq)}>
                  Answer: {m.author}: {m.text.slice(0, 40)}
                </option>
              ))}
            </Select>
          )}
        </div>
      )}
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
            emptyText="No replies yet. Reply below to start the thread."
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
