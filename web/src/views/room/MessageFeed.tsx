import { type ReactNode, useMemo, useState } from "react";
import { DesktopConnectBody } from "@/views/DesktopConnect";
import { CaretRightIcon, RocketLaunchIcon, TreeStructureIcon, UsersThreeIcon } from "@phosphor-icons/react";
import type { AgentJob } from "@/lib/api";
import { cn } from "@/lib/cn";
import { READING_MEASURE } from "@/views/room/types";
import { Button } from "@/ui/Button";
import { Skeleton } from "@/ui/Card";
import { AgentJobStatus } from "@/views/AgentJobStatus";
import { MessageDeliverables } from "@/views/workbench/MessageDeliverables";
import { placeDeliverables } from "@/lib/deliverablePlacement";
import { AgentWorking } from "@/views/room/AgentWorking";
import { Message, sameSender } from "@/views/room/Message";
import type { FeedItem } from "@/views/room/types";
import { blockProgress, blockTitle, layoutTurns, renderOrder, type TurnBlock } from "@/views/room/feedTurns";
import type { PlacedDeliverables } from "@/lib/deliverablePlacement";

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
  showJobDiagnostics,
  artifactsBySeq,
  showDesktopHandoff,
  onStartAgent,
  onLoadEarlier,
  onRetry,
  onDelete,
  onEdit,
  onCancelJob,
  queueAhead,
  onOpenArtifactInFiles,
  onViewAllDeliverables,
  people = false,
  onAskAgent,
  agentReady = false,
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
  readonly showJobDiagnostics: boolean;
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
  if (people && feed.length === 0) {
    return (
      <div className="mx-auto mt-10 flex max-w-xs flex-col items-center gap-1 text-center text-sm text-[var(--color-fg-muted)]">
        <p className="font-medium text-[var(--color-fg)]">Talk with the people in this room</p>
        <p>Agents can't see this conversation. Use “Ask the agent” on a message to bring it into the room.</p>
      </div>
    );
  }
  if (feed.length === 0 && working.size === 0 && agentReady) {
    return (
      <div className="mx-auto mt-10 flex max-w-md flex-col items-center gap-1 text-center text-sm text-[var(--color-fg-muted)]">
        <p className="font-medium text-[var(--color-fg)]">This room is ready</p>
        <p>Ask the agent anything below, or start from an example.</p>
      </div>
    );
  }
  if (feed.length === 0 && working.size === 0) {
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
    <div data-room-message-feed className={cn("flex flex-col gap-1.5", READING_MEASURE)}>
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
          showJobDiagnostics={showJobDiagnostics}
          deliverablesAfter={deliverablesAfter}
          onRetry={onRetry}
          onDelete={onDelete}
          onEdit={onEdit}
          onCancelJob={onCancelJob}
          queueAhead={queueAhead}
          onOpenArtifactInFiles={onOpenArtifactInFiles}
          onViewAllDeliverables={onViewAllDeliverables}
          onAskAgent={onAskAgent}
          people={people}
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
  readonly showJobDiagnostics: boolean;
  readonly deliverablesAfter: Map<number, PlacedDeliverables[]>;
  readonly onRetry: (item: FeedItem) => void;
  readonly onDelete: (item: FeedItem) => void;
  readonly onEdit: (item: FeedItem, text: string) => void;
  readonly onCancelJob: (jobId: string) => void;
  readonly queueAhead?: (job: AgentJob) => number;
  readonly onOpenArtifactInFiles: (artifactId: string) => void;
  readonly onViewAllDeliverables?: (seq: number) => void;
  readonly onAskAgent?: (item: FeedItem) => void;
  readonly people: boolean;
};

// FeedRows renders the feed in turn order: each request, the delegated work
// it started as one collapsible block (feedTurns.ts), then the rest.
function FeedRows({ feed, unreadFromSeq, deliverablesAfter, ...actions }: RowActions & { readonly feed: FeedItem[]; readonly unreadFromSeq: number | null }) {
  const entries = useMemo(() => layoutTurns(feed), [feed]);
  // The row rendered just before each row (blocks expanded): the unread
  // divider and sender grouping follow what is on screen.
  const prevOf = useMemo(() => {
    const order = renderOrder(entries);
    const out = new Map<number, number>();
    order.forEach((index, k) => out.set(index, k > 0 ? order[k - 1] : -1));
    return out;
  }, [entries]);
  // A member's open/closed choice per block (keyed by its request); unset
  // follows the block's state (open while its work runs).
  const [toggled, setToggled] = useState<Record<number, boolean>>({});
  const row = (i: number) => {
    const m = feed[i];
    const seq = typeof m.seq === "number" ? Math.floor(m.seq) : 0;
    const p = prevOf.get(i) ?? -1;
    const prev = p >= 0 ? feed[p] : null;
    const prevSeq = prev && typeof prev.seq === "number" ? Math.floor(prev.seq) : 0;
    // The divider sits above the first message at or past the baseline, so
    // a member landing in the channel sees where they stopped reading
    // instead of guessing.
    const startsUnread = unreadFromSeq !== null && seq >= unreadFromSeq && (p < 0 || prevSeq < unreadFromSeq);
    return (
      <FeedRow
        key={m.id}
        item={m}
        seq={seq}
        startsUnread={startsUnread}
        grouped={!!prev && !startsUnread && sameSender(prev, m)}
        placed={deliverablesAfter.get(i) ?? []}
        {...actions}
      />
    );
  };
  return (
    <>
      {entries.map((e) => {
        if (e.kind === "row") return row(e.index);
        const anchor = feed[e.block.anchor];
        const statusOf = (i: number) => {
          const s = feed[i].seq;
          return typeof s === "number" ? actions.agentJobs[Math.floor(s)]?.[0]?.status : undefined;
        };
        const progress = blockProgress(e.block, statusOf);
        const open = toggled[e.block.anchor] ?? progress.active;
        return (
          <DelegatedWork
            key={`block-${anchor.id}`}
            block={e.block}
            progress={progress}
            open={open}
            onToggle={() => setToggled((cur) => ({ ...cur, [e.block.anchor]: !open }))}
          >
            {e.block.rows.map((r) => (
              <div key={feed[r.index].id} style={r.level > 0 ? { marginLeft: `${Math.min(r.level, 3) * 1.25}rem` } : undefined}>
                {row(r.index)}
              </div>
            ))}
          </DelegatedWork>
        );
      })}
    </>
  );
}

// FeedRow is one message with its job status line and the deliverable cards
// placed after it.
function FeedRow({
  item: m,
  seq,
  startsUnread,
  grouped,
  placed,
  roomId,
  agentJobs,
  showJobDiagnostics,
  onRetry,
  onDelete,
  onEdit,
  onCancelJob,
  queueAhead,
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
        onRetry={onRetry}
        onDelete={onDelete}
        onEdit={onEdit}
        onAskAgent={onAskAgent}
        showPeopleOnly={!people}
      />
      {typeof m.seq === "number" && (
        <AgentJobStatus
          jobs={agentJobs[Math.floor(m.seq)] || []}
          showDiagnostics={showJobDiagnostics}
          onCancel={onCancelJob}
          queueAhead={queueAhead}
        />
      )}
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
  onToggle,
  children,
}: {
  readonly block: TurnBlock;
  readonly progress: { readonly done: number; readonly total: number; readonly active: boolean };
  readonly open: boolean;
  readonly onToggle: () => void;
  readonly children: ReactNode;
}) {
  const team = block.roles.length > 0;
  const Icon = team ? UsersThreeIcon : TreeStructureIcon;
  return (
    <div data-turn-block={team ? "team" : "delegated"} className="ml-10 my-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)]">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="tactile flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-xs text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
      >
        <CaretRightIcon size={12} className={cn("shrink-0 transition-transform", open && "rotate-90")} />
        <Icon size={13} className="shrink-0 text-[var(--color-accent)]" />
        <span className="min-w-0 truncate font-medium text-[var(--color-fg)]">{blockTitle(block)}</span>
        <span className="ml-auto shrink-0">
          {progress.active ? `${progress.done} of ${progress.total} done` : progress.total === 1 ? "Done" : `All ${progress.total} done`}
        </span>
      </button>
      {open && <div className="flex flex-col gap-1.5 border-t border-[var(--color-border)] px-2 py-1.5">{children}</div>}
    </div>
  );
}
