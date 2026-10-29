import { type ReactNode, useMemo, useState } from "react";
import { DesktopConnectBody } from "@/views/DesktopConnect";
import { CaretRightIcon, RocketLaunchIcon, TreeStructureIcon, UsersThreeIcon } from "@phosphor-icons/react";
import type { AgentJob } from "@/lib/api";
import { cn } from "@/lib/cn";
import { READING_MEASURE } from "@/views/room/types";
import { Button } from "@/ui/Button";
import { Skeleton } from "@/ui/Card";
import { AgentJobStatus } from "@/views/AgentJobStatus";
import { WaitingLive } from "@/views/room/JobProgressStrip";
import { MessageDeliverables } from "@/views/workbench/MessageDeliverables";
import { placeDeliverables } from "@/lib/deliverablePlacement";
import { feedEmptyKind } from "@/lib/feedEmpty";
import { AgentWorking } from "@/views/room/AgentWorking";
import { Message, sameSender } from "@/views/room/Message";
import type { FeedItem } from "@/views/room/types";
import {
  aboutRequest,
  blockProgress,
  blockTitle,
  layoutRequests,
  layoutTurns,
  progressText,
  renderOrder,
  renderRuns,
  unreadDividerAt,
  type BlockProgress,
  type RequestBlock,
  type TurnBlock,
} from "@/views/room/feedTurns";
import { FeedRequestBlock, useOpenBlocks } from "@/views/room/FeedRequestBlock";
import type { PlacedDeliverables } from "@/lib/deliverablePlacement";
import { FeedLayoutRequest, FeedLayoutTime, type FeedLayout } from "@/lib/wire.gen";

export function MessageFeed({
  loaded,
  roomId,
  feed,
  visibleFeed,
  working,
  query,
  search,
  agentTarget,
  agentIsRemoteGateway,
  starting,
  hasMoreHistory,
  loadingHistory,
  unreadFromSeq,
  agentJobs,
  artifactsBySeq,
  showDesktopHandoff,
  onStartAgent,
  onLoadEarlier,
  onRetry,
  onDelete,
  onEdit,
  onCancelJob,
  queueAhead,
  behind,
  onOpenArtifactInFiles,
  onViewAllDeliverables,
  people = false,
  onAskAgent,
  agentReady = false,
  preview = false,
  layout = FeedLayoutTime,
  emptyText,
}: {
  readonly loaded: boolean;
  readonly roomId: string;
  readonly feed: FeedItem[];
  readonly visibleFeed: FeedItem[];
  readonly working: Map<string, string>;
  readonly query: string;
  readonly search: string;
  readonly agentTarget: string;
  readonly agentIsRemoteGateway: boolean;
  readonly starting: boolean;
  readonly hasMoreHistory: boolean;
  readonly loadingHistory: boolean;
  readonly unreadFromSeq: number | null;
  readonly agentJobs: Record<number, AgentJob[]>;
  readonly artifactsBySeq: Record<number, string[]>;
  readonly showDesktopHandoff: boolean;
  readonly onStartAgent: () => void;
  readonly onLoadEarlier: () => void;
  readonly onRetry: (item: FeedItem) => void;
  readonly onDelete: (item: FeedItem) => void;
  readonly onEdit: (item: FeedItem, text: string) => void;
  readonly onCancelJob: (jobId: string) => void;
  // Jobs queued before a job on its platform host ("Queued — N ahead").
  readonly queueAhead?: (job: AgentJob) => number;
  readonly behind?: (job: AgentJob) => AgentJob | undefined;
  readonly onOpenArtifactInFiles: (artifactId: string) => void;
  // "View all deliverables (N)" under a message: the workbench's deliverables
  // list filtered to that message.
  readonly onViewAllDeliverables?: (seq: number) => void;
  // The people chat's feed: its own empty state, no people-only markers
  // (every message there is), and "Ask the agent about this" on messages.
  readonly people?: boolean;
  readonly onAskAgent?: (item: FeedItem) => void;
  // The room already has an agent to talk to (e.g. a platform agent): the
  // empty state points at the composer and its example prompts instead of
  // agent setup.
  readonly agentReady?: boolean;
  // The reader has not joined this shared room (a preview): an empty feed
  // says so instead of calling the room quiet.
  readonly preview?: boolean;
  // How the member reads the feed (useFeedLayout): By time or By request.
  readonly layout?: FeedLayout;
  // What an empty feed says instead of the room's empty state (a thread with
  // no replies yet: it is not a room, and its root is shown above it).
  readonly emptyText?: string;
}) {
  // A turn's cards sit under its last reply (their sourceSeq is the request).
  const deliverablesAfter = useMemo(
    () => placeDeliverables(visibleFeed, artifactsBySeq, agentJobs),
    [visibleFeed, artifactsBySeq, agentJobs],
  );
  if (!loaded) {
    return (
      <div className={cn("flex flex-col gap-3", READING_MEASURE)}>
        <Skeleton className="h-12 w-2/3" />
        <Skeleton className="h-12 w-1/2 self-end" />
        <Skeleton className="h-12 w-3/5" />
      </div>
    );
  }
  const empty = feed.length === 0 && working.size === 0 ? feedEmptyKind({ people, preview, agentReady }) : null;
  if (emptyText && feed.length === 0 && working.size === 0) {
    return <p className="mt-6 text-center text-sm text-[var(--color-fg-subtle)]">{emptyText}</p>;
  }
  if (people && feed.length === 0) {
    return (
      <div className="mx-auto mt-10 flex max-w-xs flex-col items-center gap-1 text-center text-sm text-[var(--color-fg-muted)]">
        <p className="font-medium text-[var(--color-fg)]">Talk with the people in this room</p>
        <p>Agents can't see this conversation. Use “Ask the agent” on a message to bring it into the room.</p>
      </div>
    );
  }
  if (empty === "preview") {
    return (
      <div className="mx-auto mt-10 flex max-w-md flex-col items-center gap-1 text-center text-sm text-[var(--color-fg-muted)]">
        <p className="font-medium text-[var(--color-fg)]">No messages yet</p>
        <p>You are reading this room before joining it. Join to start the conversation.</p>
      </div>
    );
  }
  if (empty === "ready") {
    return (
      <div className="mx-auto mt-10 flex max-w-md flex-col items-center gap-1 text-center text-sm text-[var(--color-fg-muted)]">
        <p className="font-medium text-[var(--color-fg)]">This room is ready</p>
        <p>Ask the agent anything below, or start from an example.</p>
      </div>
    );
  }
  if (empty === "quiet") {
    return (
      <div className="mx-auto mt-10 flex max-w-md flex-col items-center gap-4 text-center text-sm text-[var(--color-fg-muted)]">
        <div>
          <p className="font-medium text-[var(--color-fg)]">This room is quiet</p>
          <p className="mt-1">
            Send a message to chat with members
            {agentTarget
              ? ", or start a local agent to collaborate."
              : agentIsRemoteGateway
                ? ". To bring your own agent, run bnbloc desktop on this gateway; you can also borrow a member’s agent with @."
                : ". Connect a local core from “Run local agent”, or borrow a member’s agent with @."}
          </p>
        </div>
        {agentTarget && (
          <Button variant="primary" onClick={onStartAgent} disabled={starting}>
            <RocketLaunchIcon size={16} />
            {starting ? "Starting…" : "Start local agent"}
          </Button>
        )}
        {showDesktopHandoff && (
          <DesktopConnectBody />
        )}
      </div>
    );
  }
  return (
    <div data-room-message-feed data-feed-layout={layout} className={cn("flex flex-col gap-1.5", READING_MEASURE)}>
      {hasMoreHistory && (
        <div className="mb-2 flex justify-center">
          <Button
            variant="ghost"
            size="sm"
            disabled={loadingHistory}
            onClick={onLoadEarlier}
          >
            {loadingHistory ? "Loading…" : "Load earlier"}
          </Button>
        </div>
      )}
      {query && visibleFeed.length === 0 ? (
        <p className="mt-6 text-center text-sm text-[var(--color-fg-subtle)]">
          No loaded messages match “{search.trim()}”. Older history is not
          searched.
        </p>
      ) : (
        <FeedRows
          roomId={roomId}
          feed={visibleFeed}
          unreadFromSeq={unreadFromSeq}
          agentJobs={agentJobs}
          deliverablesAfter={deliverablesAfter}
          onRetry={onRetry}
          onDelete={onDelete}
          onEdit={onEdit}
          onCancelJob={onCancelJob}
          queueAhead={queueAhead}
          behind={behind}
          onOpenArtifactInFiles={onOpenArtifactInFiles}
          onViewAllDeliverables={onViewAllDeliverables}
          onAskAgent={onAskAgent}
          people={people}
          layout={layout}
        />
      )}
      {[...working].map(([key, label]) => (
        <AgentWorking key={key} label={label} />
      ))}
    </div>
  );
}

type RowActions = {
  readonly roomId: string;
  readonly agentJobs: Record<number, AgentJob[]>;
  readonly deliverablesAfter: Map<number, PlacedDeliverables[]>;
  readonly onRetry: (item: FeedItem) => void;
  readonly onDelete: (item: FeedItem) => void;
  readonly onEdit: (item: FeedItem, text: string) => void;
  readonly onCancelJob: (jobId: string) => void;
  readonly queueAhead?: (job: AgentJob) => number;
  readonly behind?: (job: AgentJob) => AgentJob | undefined;
  readonly onOpenArtifactInFiles: (artifactId: string) => void;
  readonly onViewAllDeliverables?: (seq: number) => void;
  readonly onAskAgent?: (item: FeedItem) => void;
  readonly people: boolean;
};

// FeedRows renders the feed in the member's layout (feedTurns.ts). By time:
// each request, the delegated work it started as one collapsible block,
// then the rest; a late notice sits back at its request's turn, naming it.
// By request: a block per request (FeedRequestBlock), the rows that answer
// no request between them.
function FeedRows({
  feed,
  unreadFromSeq,
  deliverablesAfter,
  layout,
  ...actions
}: RowActions & { readonly feed: FeedItem[]; readonly unreadFromSeq: number | null; readonly layout: FeedLayout }) {
  const byRequest = layout === FeedLayoutRequest;
  const entries = useMemo(() => (byRequest ? layoutRequests(feed) : layoutTurns(feed)), [feed, byRequest]);
  // The row rendered just before each row in its run (blocks expanded):
  // sender grouping follows what is on screen.
  const prevOf = useMemo(() => {
    const out = new Map<number, number>();
    for (const run of renderRuns(entries)) run.forEach((index, k) => out.set(index, k > 0 ? run[k - 1] : -1));
    return out;
  }, [entries]);
  // The divider sits above the first row of the unread run that ends what
  // is on screen (a request's folded steps are not), so a member landing in
  // the channel sees where they stopped reading instead of guessing.
  const dividerAt = useMemo(
    () => unreadDividerAt(renderOrder(entries, { steps: !byRequest }), (i) => feed[i].seq ?? 0, unreadFromSeq),
    [entries, feed, unreadFromSeq, byRequest],
  );
  // A member's open/closed choice per delegated-work block (keyed by its
  // request); unset follows the block's state (open while its work runs).
  const [toggled, setToggled] = useState<Record<number, boolean>>({});
  // The request blocks the member opened (folded by default).
  const openBlocks = useOpenBlocks(actions.roomId);
  const row = (i: number, about?: number, bare = false) => {
    const m = feed[i];
    const seq = m.seq ?? 0;
    const p = prevOf.get(i) ?? -1;
    const prev = p >= 0 ? feed[p] : null;
    const startsUnread = i === dividerAt;
    return (
      <FeedRow
        key={m.id}
        item={m}
        seq={seq}
        startsUnread={startsUnread}
        grouped={!!prev && !startsUnread && sameSender(prev, m)}
        placed={bare ? [] : (deliverablesAfter.get(i) ?? [])}
        about={about === undefined ? undefined : aboutRequest(feed[about].text)}
        bare={bare}
        {...actions}
      />
    );
  };
  const delegated = (block: TurnBlock, bare = false) => {
    const anchor = feed[block.anchor];
    const statusOf = (i: number) => {
      const s = feed[i].seq;
      return s != null ? actions.agentJobs[s]?.[0]?.status : undefined;
    };
    const progress = blockProgress(block, statusOf);
    const open = toggled[block.anchor] ?? progress.active;
    return (
      <DelegatedWork
        key={`block-${anchor.id}`}
        block={block}
        progress={progress}
        open={open}
        inset={!bare}
        onToggle={() => setToggled((cur) => ({ ...cur, [block.anchor]: !open }))}
      >
        {block.rows.map((r) => (
          <div key={feed[r.index].id} style={r.level > 0 ? { marginLeft: `${Math.min(r.level, 3) * 1.25}rem` } : undefined}>
            {row(r.index, undefined, bare)}
          </div>
        ))}
      </DelegatedWork>
    );
  };
  const request = (r: RequestBlock) => {
    const ask = feed[r.anchor];
    const seq = ask.seq;
    const jobs = typeof seq === "number" ? actions.agentJobs[seq] || [] : [];
    // Every card of the turn sits at the block's foot.
    const placed = [r.anchor, ...r.stepRows, ...r.foot].flatMap((i) => deliverablesAfter.get(i) ?? []);
    return (
      <FeedRequestBlock
        key={`request-${ask.id}`}
        request={r}
        feed={feed}
        jobs={jobs}
        open={openBlocks.isOpen(seq)}
        onToggle={() => openBlocks.toggle(seq)}
        onCancelJob={actions.onCancelJob}
        queueAhead={actions.queueAhead}
        behind={actions.behind}
        renderRow={(i) => row(i, undefined, true)}
        renderStep={(st) => (st.kind === "row" ? row(st.index, undefined, true) : delegated(st.block, true))}
        foot={
          <>
            <WaitingLive jobs={jobs} />
            {placed.map((d) => (
              <MessageDeliverables
                key={d.seq}
                roomId={actions.roomId}
                seq={d.seq}
                artifactIds={d.artifactIds}
                onOpen={actions.onOpenArtifactInFiles}
                onViewAll={actions.onViewAllDeliverables ?? (() => {})}
              />
            ))}
          </>
        }
      />
    );
  };
  return (
    <>
      {entries.map((e) => (e.kind === "row" ? row(e.index, e.about) : e.kind === "block" ? delegated(e.block) : request(e.request)))}
    </>
  );
}

// FeedRow is one message with its job status line and the deliverable cards
// placed after it; a bare row (inside a request block, which draws those
// itself) is the message alone.
function FeedRow({
  item: m,
  seq,
  startsUnread,
  grouped,
  placed,
  about,
  bare,
  roomId,
  agentJobs,
  onRetry,
  onDelete,
  onEdit,
  onCancelJob,
  queueAhead,
  behind,
  onOpenArtifactInFiles,
  onViewAllDeliverables,
  onAskAgent,
  people,
}: Omit<RowActions, "deliverablesAfter"> & {
  readonly item: FeedItem;
  readonly seq: number;
  readonly startsUnread: boolean;
  readonly grouped: boolean;
  readonly placed: readonly PlacedDeliverables[];
  // A late notice's line naming the request it is about.
  readonly about?: string;
  readonly bare: boolean;
}) {
  return (
    <div data-seq={seq || undefined}>
      {startsUnread && (
        <div className="my-2 flex items-center gap-2" aria-label="New messages">
          <span className="h-px flex-1 bg-[var(--color-accent)] opacity-40" />
          <span className="text-[11px] font-semibold uppercase tracking-wide text-[var(--color-accent)]">
            New
          </span>
          <span className="h-px flex-1 bg-[var(--color-accent)] opacity-40" />
        </div>
      )}
      <Message
        roomId={roomId}
        item={m}
        grouped={grouped}
        about={about}
        onRetry={onRetry}
        onDelete={onDelete}
        onEdit={onEdit}
        onAskAgent={onAskAgent}
        showPeopleOnly={!people}
      />
      {!bare && typeof m.seq === "number" && (
        <AgentJobStatus
          jobs={agentJobs[m.seq] || []}
          onCancel={onCancelJob}
          queueAhead={queueAhead}
          behind={behind}
        />
      )}
      {!bare && typeof m.seq === "number" && <WaitingLive jobs={agentJobs[m.seq] || []} />}
      {placed.map((d) => (
        <MessageDeliverables
          key={d.seq}
          roomId={roomId}
          seq={d.seq}
          artifactIds={d.artifactIds}
          onOpen={onOpenArtifactInFiles}
          onViewAll={onViewAllDeliverables ?? (() => {})}
        />
      ))}
    </div>
  );
}

// DelegatedWork is the collapsible block of work a request's agent handed
// to other agents: "Team · writer, critic" (a team's roles) or "Delegated
// to bob/claude", with how many of the dispatched tasks have finished.
function DelegatedWork({
  block,
  progress,
  open,
  inset,
  onToggle,
  children,
}: {
  readonly block: TurnBlock;
  readonly progress: BlockProgress;
  readonly open: boolean;
  // Indented under its request (By time); flush inside a request's steps.
  readonly inset: boolean;
  readonly onToggle: () => void;
  readonly children: ReactNode;
}) {
  const team = block.roles.length > 0;
  const Icon = team ? UsersThreeIcon : TreeStructureIcon;
  return (
    <div data-turn-block={team ? "team" : "delegated"} className={cn("my-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)]", inset && "ml-10")}>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="tactile flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-xs text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
      >
        <CaretRightIcon size={12} className={cn("shrink-0 transition-transform", open && "rotate-90")} />
        <Icon size={13} className="shrink-0 text-[var(--color-accent)]" />
        <span className="min-w-0 truncate font-medium text-[var(--color-fg)]">{blockTitle(block)}</span>
        <span className="ml-auto shrink-0">{progressText(progress)}</span>
      </button>
      {open && <div className="flex flex-col gap-1.5 border-t border-[var(--color-border)] px-2 py-1.5">{children}</div>}
    </div>
  );
}
