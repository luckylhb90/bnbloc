import { useState } from "react";
import { CheckCircleIcon, ChatsCircleIcon, KanbanIcon, ListBulletsIcon, PlusIcon } from "@phosphor-icons/react";
import type { Channel } from "@/lib/api";
import { channelsApi } from "@/lib/channelsApi";
import { cn } from "@/lib/cn";
import { errorText } from "@/lib/errors";
import { boardColumns, forumFiltered, forumOrder, type ForumFilter } from "@/lib/forum";
import { relativeTime } from "@/lib/format";
import { MaxThreadTitle, MaxThreadTags, type ChannelSettings, type RoomChannel } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Input, Textarea } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";

type Layout = "list" | "board";

// ForumView is a forum channel's main view (docs/prd/2026-10-28-channels-
// and-threads.md §2.1c): its threads, open first by activity, filtered by
// tag and status, as a list or as a board of its statuses; a new post starts
// from the channel's template and opens its own thread. Opening a thread
// shows it in the side panel (ThreadPanel), where its tags, status and
// answer are set.
export function ForumView({
  roomId,
  channel,
  threads,
  unreadByChannel,
  mayPost,
  onOpen,
  onChanged,
}: {
  readonly roomId: string;
  readonly channel: Channel;
  // The channel's threads (useRoomThreads: every thread of this parent).
  readonly threads: readonly RoomChannel[];
  readonly unreadByChannel: Readonly<Record<string, number>>;
  readonly mayPost: boolean;
  readonly onOpen: (thread: RoomChannel) => void;
  // A post opened a thread, or a card moved: the listing reloads.
  readonly onChanged: (opened?: string) => void;
}) {
  const s: ChannelSettings = channel.settings ?? {};
  const [layout, setLayout] = useState<Layout>("list");
  const [filter, setFilter] = useState<ForumFilter>({});
  const [composing, setComposing] = useState(false);
  const shown = forumFiltered(forumOrder(threads), filter);
  const tagName = (id: string) => s.tags?.find((t) => t.id === id)?.name ?? id;
  const statusName = (id: string) => s.statuses?.find((t) => t.id === id)?.name ?? "";

  return (
    <div data-forum-view className="flex min-h-0 flex-1 flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-[var(--color-border)] px-4 py-2">
        <div role="radiogroup" aria-label="Show threads as" className="flex rounded-[var(--radius)] bg-[var(--color-surface-2)] p-0.5">
          {(["list", "board"] as const).map((l) => (
            <button
              key={l}
              type="button"
              role="radio"
              aria-checked={layout === l}
              data-forum-layout={l}
              onClick={() => setLayout(l)}
              className={cn(
                "tactile flex h-8 items-center gap-1.5 rounded-[calc(var(--radius)-2px)] px-2 text-xs font-medium",
                layout === l ? "bg-[var(--color-elevated)] text-[var(--color-fg)] shadow-sm" : "text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]",
              )}
            >
              {l === "list" ? <ListBulletsIcon size={14} /> : <KanbanIcon size={14} />}
              {l === "list" ? "List" : "Board"}
            </button>
          ))}
        </div>
        {(s.tags?.length ?? 0) > 0 && (
          <Select
            aria-label="Filter by tag"
            value={filter.tag ?? ""}
            onChange={(e) => setFilter((f) => ({ ...f, tag: e.target.value || undefined }))}
            className="h-8 w-auto text-xs"
          >
            <option value="">All tags</option>
            {s.tags!.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </Select>
        )}
        {layout === "list" && (s.statuses?.length ?? 0) > 0 && (
          <Select
            aria-label="Filter by status"
            value={filter.status ?? ""}
            onChange={(e) => setFilter((f) => ({ ...f, status: e.target.value || undefined }))}
            className="h-8 w-auto text-xs"
          >
            <option value="">All statuses</option>
            {s.statuses!.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </Select>
        )}
        <span className="flex-1" />
        {mayPost && (
          <Button variant="primary" size="sm" data-forum-new onClick={() => setComposing(true)}>
            <PlusIcon size={14} /> New post
          </Button>
        )}
      </div>
      {composing && (
        <NewPost
          roomId={roomId}
          channel={channel}
          onCancel={() => setComposing(false)}
          onPosted={(threadId) => {
            setComposing(false);
            onChanged(threadId);
          }}
        />
      )}
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        {threads.length === 0 ? (
          <p className="mt-10 text-center text-sm text-[var(--color-fg-subtle)]">No posts yet. {mayPost ? "Start one with New post." : ""}</p>
        ) : layout === "list" ? (
          <ul className="flex flex-col gap-1.5">
            {shown.map((t) => (
              <li key={t.channelId}>
                <ThreadRow thread={t} unread={unreadByChannel[t.channelId] ?? 0} tagName={tagName} statusName={statusName} onOpen={() => onOpen(t)} />
              </li>
            ))}
            {shown.length === 0 && <li className="text-sm text-[var(--color-fg-subtle)]">No thread matches the filter.</li>}
          </ul>
        ) : (
          <Board roomId={roomId} channel={channel} threads={forumFiltered(threads, { tag: filter.tag })} mayPost={mayPost} tagName={tagName} onOpen={onOpen} onChanged={() => onChanged()} />
        )}
      </div>
    </div>
  );
}

function ThreadRow({
  thread: t,
  unread,
  tagName,
  statusName,
  onOpen,
}: {
  readonly thread: RoomChannel;
  readonly unread: number;
  readonly tagName: (id: string) => string;
  readonly statusName: (id: string) => string;
  readonly onOpen: () => void;
}) {
  const info = t.thread;
  const closed = (t.archivedAtMs ?? 0) > 0;
  return (
    <button
      type="button"
      data-forum-thread={t.name}
      onClick={onOpen}
      className={cn(
        "tactile flex w-full min-w-0 flex-col gap-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-left hover:border-[var(--color-border-strong)]",
        closed && "opacity-60",
      )}
    >
      <span className="flex min-w-0 items-center gap-2">
        {info?.answeredSeq ? <CheckCircleIcon size={14} weight="fill" className="shrink-0 text-[var(--color-success,#22c55e)]" aria-label="Answered" /> : null}
        <span className={cn("min-w-0 flex-1 truncate text-sm", unread > 0 ? "font-semibold" : "font-medium")}>{t.name}</span>
        {unread > 0 && <span className="shrink-0 rounded-full bg-[var(--color-accent)] px-1.5 text-[11px] font-semibold leading-5 text-[var(--color-accent-fg,#04120c)]">{unread}</span>}
      </span>
      <span className="flex flex-wrap items-center gap-1.5 text-xs text-[var(--color-fg-subtle)]">
        {info?.status && statusName(info.status) && (
          <span className="rounded-full border border-[var(--color-border)] px-1.5">{statusName(info.status)}</span>
        )}
        {(info?.tags ?? []).map((id) => (
          <span key={id} className="rounded-full bg-[var(--color-surface-2)] px-1.5">
            {tagName(id)}
          </span>
        ))}
        <ChatsCircleIcon size={12} />
        <span>{info?.replies ?? 0}</span>
        {info?.lastReplyAtMs ? <span>· {relativeTime(info.lastReplyAtMs)}</span> : null}
        {closed && <span>· closed</span>}
      </span>
    </button>
  );
}

// Board is the forum by status: a column per status (lib/forum
// boardColumns), a card per thread; a card's menu moves it to another
// status (the thread's mark).
function Board({
  roomId,
  channel,
  threads,
  mayPost,
  tagName,
  onOpen,
  onChanged,
}: {
  readonly roomId: string;
  readonly channel: Channel;
  readonly threads: readonly RoomChannel[];
  readonly mayPost: boolean;
  readonly tagName: (id: string) => string;
  readonly onOpen: (t: RoomChannel) => void;
  readonly onChanged: () => void;
}) {
  const toast = useToast();
  const statuses = channel.settings?.statuses ?? [];
  const move = (t: RoomChannel, status: string) =>
    channelsApi.markThread(roomId, t.channelId, { status }).then(onChanged, (e: unknown) => toast.error(errorText(e, "Could not move the thread")));
  if (statuses.length === 0) {
    return <p className="mt-10 text-center text-sm text-[var(--color-fg-subtle)]">The board shows the channel's statuses: its owners set them in the channel's settings.</p>;
  }
  return (
    <div data-forum-board className="flex min-h-full gap-3 overflow-x-auto pb-2">
      {boardColumns(statuses, threads).map((col) => (
        <section key={col.id || "none"} data-board-column={col.name} className="flex w-64 shrink-0 flex-col gap-2 rounded-[var(--radius-lg)] bg-[var(--color-surface-2)] p-2">
          <h3 className="px-1 text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
            {col.name} <span className="font-normal">{col.threads.length}</span>
          </h3>
          {col.threads.map((t) => (
            <div key={t.channelId} data-board-card={t.name} className="flex flex-col gap-1.5 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-2">
              <button type="button" onClick={() => onOpen(t)} className="tactile text-left text-sm font-medium hover:underline">
                {t.name}
              </button>
              {(t.thread?.tags?.length ?? 0) > 0 && (
                <span className="flex flex-wrap gap-1 text-[11px] text-[var(--color-fg-subtle)]">
                  {t.thread!.tags!.map((id) => (
                    <span key={id} className="rounded-full bg-[var(--color-surface-2)] px-1.5">
                      {tagName(id)}
                    </span>
                  ))}
                </span>
              )}
              {mayPost && (
                <Select aria-label={`Move ${t.name}`} value={t.thread?.status ?? ""} onChange={(e) => void move(t, e.target.value)} className="h-7 text-xs">
                  <option value="">No status</option>
                  {statuses.map((st) => (
                    <option key={st.id} value={st.id}>
                      {st.name}
                    </option>
                  ))}
                </Select>
              )}
            </div>
          ))}
        </section>
      ))}
    </div>
  );
}

// NewPost is a forum's new post: a title, the body from the channel's
// template, and its tags; posting it opens its thread (the server makes the
// thread whose root it is and names it).
function NewPost({
  roomId,
  channel,
  onCancel,
  onPosted,
}: {
  readonly roomId: string;
  readonly channel: Channel;
  readonly onCancel: () => void;
  readonly onPosted: (threadId?: string) => void;
}) {
  const toast = useToast();
  const s = channel.settings ?? {};
  const [title, setTitle] = useState("");
  const [text, setText] = useState(s.postTemplate ?? "");
  const [tags, setTags] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const post = async () => {
    if (!text.trim()) return;
    setBusy(true);
    try {
      const r = await channelsApi.postToForum(roomId, channel.channelId, { text: text.trim(), threadTitle: title.trim() || undefined, threadTags: tags });
      onPosted(r.threadId || undefined);
    } catch (e) {
      toast.error(errorText(e, "Could not post"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div data-forum-compose className="flex flex-col gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3">
      <Input aria-label="Post title" placeholder="Title" maxLength={MaxThreadTitle} value={title} onChange={(e) => setTitle(e.target.value)} />
      <Textarea aria-label="Post" rows={6} value={text} onChange={(e) => setText(e.target.value)} placeholder="What is it about?" />
      {(s.tags?.length ?? 0) > 0 && (
        <div className="flex flex-wrap gap-1.5" role="group" aria-label="Tags">
          {s.tags!.map((t) => {
            const on = tags.includes(t.id);
            return (
              <button
                key={t.id}
                type="button"
                aria-pressed={on}
                disabled={!on && tags.length >= MaxThreadTags}
                onClick={() => setTags((cur) => (on ? cur.filter((x) => x !== t.id) : [...cur, t.id]))}
                className={cn(
                  "tactile rounded-full border px-2 py-0.5 text-xs",
                  on ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)] text-[var(--color-fg)]" : "border-[var(--color-border)] text-[var(--color-fg-subtle)]",
                )}
              >
                {t.name}
              </button>
            );
          })}
        </div>
      )}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button variant="primary" size="sm" disabled={busy || !text.trim()} onClick={() => void post()}>
          Post
        </Button>
      </div>
    </div>
  );
}
