import { useCallback, useId, useState, type ReactNode } from "react";
import { CaretRightIcon } from "@phosphor-icons/react";
import type { AgentJob } from "@/lib/api";
import { isLiveJob } from "@/lib/agentRuns";
import { useAuth } from "@/lib/auth";
import { cn } from "@/lib/cn";
import { gatewayBase } from "@/lib/config";
import { currentStep, formatElapsed, stepLabel } from "@/lib/jobProgress";
import { agentHandle } from "@/lib/roomAgents";
import { memberKey, readStored, StorageKey, writeStored } from "@/lib/storageKeys";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { Dot } from "@/ui/Card";
import { AgentJobStatus } from "@/views/AgentJobStatus";
import { ActorLabel } from "@/views/room/ActorLabel";
import { agentJobStatusLabel, steeredNote } from "@/views/room/agentJobLabels";
import { latestStep, requestElapsed, requestState, stepCountText, type RequestBlock, type RequestState } from "@/views/room/feedTurns";
import { useJobProgress } from "@/views/room/JobProgressStrip";
import type { FeedItem } from "@/views/room/types";

// The block of one request in the By request feed
// (docs/prd/2026-10-21-feed-by-request.md): the request, a head that says
// how it is going (the agent, a status dot, the status and elapsed time,
// the latest step while it works, the step count), the steps folded under
// the head, and the foot (the answer, every other notice, the deliverables)
// always shown. feedTurns.ts decides what goes where; this only draws it.

// The head's status light per state: only a live request pulses.
const DOT: Record<RequestState, { readonly tone: "accent" | "muted" | "danger"; readonly still: boolean }> = {
  live: { tone: "accent", still: false },
  done: { tone: "accent", still: true },
  failed: { tone: "danger", still: true },
  none: { tone: "muted", still: true },
};

// The job the head follows: a live one, else the newest the list holds.
const headJob = (jobs: readonly AgentJob[]) => jobs.find((j) => isLiveJob(j)) ?? jobs[0];

export function FeedRequestBlock({
  request,
  feed,
  jobs,
  open,
  onToggle,
  onCancelJob,
  queueAhead,
  behind,
  renderRow,
  renderStep,
  foot,
}: {
  readonly request: RequestBlock;
  readonly feed: readonly FeedItem[];
  // The request's jobs (the room's job list by its sequence).
  readonly jobs: AgentJob[];
  readonly open: boolean;
  readonly onToggle: () => void;
  readonly onCancelJob: (jobId: string) => void;
  readonly queueAhead?: (job: AgentJob) => number;
  readonly behind?: (job: AgentJob) => AgentJob | undefined;
  // One feed row without its job line or cards (the block draws those).
  readonly renderRow: (index: number) => ReactNode;
  // One step: a row, or the delegated work.
  readonly renderStep: (step: RequestBlock["steps"][number]) => ReactNode;
  // What sits under the foot's rows: the deliverables, a live view waiting
  // for a person.
  readonly foot: ReactNode;
}) {
  const stepsId = useId();
  const ask = feed[request.anchor];
  const footRows = request.foot.map((i) => feed[i]);
  const state = requestState(jobs, footRows);
  const job = headJob(jobs);
  const live = state === "live";
  const progress = useJobProgress(live ? job?.jobId : undefined);
  const [, setTick] = useState(0);
  useVisiblePoll(() => setTick((t) => t + 1), 1000, live);
  const now = Date.now();
  const answeredAtMs = footRows.find((r) => r.author === "agent" && !r.notice)?.createdAtMs;
  const elapsed = requestElapsed(state, job, progress, { askedAtMs: ask.createdAtMs, answeredAtMs }, now);
  const current = currentStep(progress);
  const lastStep = request.stepRows.length > 0 ? feed[request.stepRows[request.stepRows.length - 1]] : undefined;
  const latest = live ? latestStep(lastStep, current ? { label: stepLabel(current), atMs: current.atMs } : undefined) : "";
  const agent = request.agent || (job?.provider ? agentHandle({ ownerId: job.ownerUserId, core: job.core || undefined, provider: job.provider }) : "");
  const status =
    state === "live" && job
      ? agentJobStatusLabel(job, queueAhead?.(job))
      : state === "failed"
        ? job
          ? agentJobStatusLabel(job)
          : "failed"
        : state === "done"
          ? (job && steeredNote(job)) || "done"
          : "";
  const count = stepCountText(request.stepRows.length);
  const stepSeqs = open ? undefined : request.stepRows.map((i) => feed[i].seq).filter((s): s is number => typeof s === "number").join(" ") || undefined;
  // A settled job's chip says how it ended, unless a notice at the foot
  // already does (the server's sentence about the same failure).
  const settledJobs = live || footRows.some((r) => r.notice) ? [] : jobs;
  return (
    <section
      data-request-block={state}
      data-step-seqs={stepSeqs}
      className="my-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)]"
    >
      <div className="px-3 pb-2 pt-1">{renderRow(request.anchor)}</div>
      <div className="flex min-w-0 items-center border-t border-[var(--color-border)]">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          aria-controls={open ? stepsId : undefined}
          disabled={request.steps.length === 0 && !live}
          className="tactile flex min-w-0 flex-1 flex-col gap-0.5 px-3 py-1.5 text-left text-xs text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-accent)]/60 disabled:cursor-default disabled:hover:text-[var(--color-fg-muted)]"
        >
          {/* One line on a wide column; on a narrow one the agent and status
              shorten, the latest step takes a second line and the count
              shows from a small width. */}
          <span className="flex w-full min-w-0 items-center gap-2">
            <Dot {...DOT[state]} />
            {agent ? <ActorLabel actor={agent} className="max-w-[45%]" /> : <span className="shrink-0 font-medium text-[var(--color-fg)]">Agent</span>}
            {(status || elapsed !== null) && (
              <span className="min-w-0 truncate tabular-nums">
                {status}
                {status && elapsed !== null ? " · " : ""}
                {elapsed !== null ? formatElapsed(elapsed) : ""}
              </span>
            )}
            <span className="hidden min-w-0 flex-1 truncate text-[var(--color-fg-subtle)] @lg:block" title={latest || undefined}>
              {latest}
            </span>
            <span className="ml-auto flex shrink-0 items-center gap-2 text-[var(--color-fg-subtle)]">
              {count && <span className="hidden @sm:inline">{count}</span>}
              <CaretRightIcon size={12} aria-hidden className={cn("transition-transform", open && "rotate-90", request.steps.length === 0 && !live && "invisible")} />
            </span>
          </span>
          {latest && (
            <span className="block w-full truncate pl-4 text-[var(--color-fg-subtle)] @lg:hidden" title={latest}>
              {latest}
            </span>
          )}
        </button>
        {live && job && (
          <button
            type="button"
            onClick={() => onCancelJob(job.jobId)}
            className="tactile mr-2 shrink-0 rounded-[var(--radius)] px-1.5 py-0.5 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
          >
            stop
          </button>
        )}
      </div>
      {open && (
        <div id={stepsId} className="flex flex-col gap-1.5 border-t border-[var(--color-border)] px-3 py-2">
          {/* A live job's chip and progress strip (what it does now). */}
          {live && <AgentJobStatus jobs={jobs} onCancel={onCancelJob} queueAhead={queueAhead} behind={behind} />}
          {request.steps.map(renderStep)}
        </div>
      )}
      {/* Hidden while nothing in it draws (no answer, outcome or card yet). */}
      <div data-request-foot className="flex flex-col gap-1.5 border-t border-[var(--color-border)] px-3 py-2 empty:hidden">
        {request.foot.map(renderRow)}
        {/* A settled job's outcome: its failure reason stays in view. */}
        <AgentJobStatus jobs={settledJobs} onCancel={onCancelJob} queueAhead={queueAhead} behind={behind} />
        {foot}
      </div>
    </section>
  );
}

// useOpenBlocks remembers which request blocks the member opened in this
// room for the browser session (sessionStorage, per member and room),
// keyed by the request's sequence.
export function useOpenBlocks(roomId: string): { readonly isOpen: (seq: number | undefined) => boolean; readonly toggle: (seq: number | undefined) => void } {
  const { subject } = useAuth();
  const key = subject ? memberKey(StorageKey.feedOpenBlocks, { gateway: gatewayBase(), subject }, roomId) : "";
  const [open, setOpen] = useState<ReadonlySet<number>>(() => readOpen(key));
  const [loadedKey, setLoadedKey] = useState(key);
  if (loadedKey !== key) {
    setLoadedKey(key);
    setOpen(readOpen(key));
  }
  const toggle = useCallback(
    (seq: number | undefined) => {
      if (typeof seq !== "number") return;
      setOpen((cur) => {
        const next = new Set(cur);
        if (next.has(seq)) next.delete(seq);
        else next.add(seq);
        if (key) writeStored(key, next.size > 0 ? JSON.stringify([...next]) : "", "session");
        return next;
      });
    },
    [key],
  );
  return { isOpen: (seq) => typeof seq === "number" && open.has(seq), toggle };
}

function readOpen(key: string): ReadonlySet<number> {
  if (!key) return new Set();
  try {
    const parsed: unknown = JSON.parse(readStored(key, "session") || "[]");
    return new Set(Array.isArray(parsed) ? parsed.filter((n): n is number => typeof n === "number") : []);
  } catch {
    return new Set();
  }
}
