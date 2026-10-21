import { createContext, useContext, useState } from "react";
import { BrainIcon, CaretDownIcon, PencilSimpleLineIcon, TerminalIcon } from "@phosphor-icons/react";
import type { AgentJob } from "@/lib/api";
import { isLiveJob } from "@/lib/agentRuns";
import { cn } from "@/lib/cn";
import {
  currentStep,
  currentStepFor,
  earlierSteps,
  formatElapsed,
  quietNote,
  stepLabel,
  turnStart,
  type JobProgress,
  type JobStep,
} from "@/lib/jobProgress";
import type { JobLive } from "@/lib/jobLive";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { JobLiveThumb, LiveWaitingNote } from "@/views/live/JobLiveView";

// A live job's progress (live-turns PRD §3) reaches the job rows through
// context, so the feed's row props stay as they are: the room's map by job
// id (useRoomActivity owns it), empty outside a room.
const JobProgressContext = createContext<Readonly<Record<string, JobProgress>>>({});
export const JobProgressProvider = JobProgressContext.Provider;

// useJobProgress is one live job's progress (undefined when none is known).
export function useJobProgress(jobId: string | undefined): JobProgress | undefined {
  const progress = useContext(JobProgressContext);
  return jobId ? progress[jobId] : undefined;
}

// A live job's live view (browser component PRD §2.8) reaches its row the
// same way: the room's map by job id, how to open the large view, and, for
// a view waiting for a person (§3.7), who is looking and how to let the
// agent continue or close the view.
interface JobLiveRoom {
  readonly roomId: string;
  readonly live: Readonly<Record<string, JobLive>>;
  readonly open?: (jobId: string) => void;
  readonly subject?: string | null;
  readonly onContinue?: (live: JobLive) => void;
}
const JobLiveContext = createContext<JobLiveRoom | null>(null);
export const JobLiveProvider = JobLiveContext.Provider;

// WaitingLive sits under a request whose turn ended while its live view
// waits for a person (a sign-in only they can do, §3.7): the page, what it
// waits for, and, for its owner, continue and close. A running job shows
// its view in its progress strip instead.
export function WaitingLive({ jobs }: { jobs: readonly AgentJob[] }) {
  const liveRoom = useContext(JobLiveContext);
  if (!liveRoom) return null;
  const waiting = jobs.filter((j) => !isLiveJob(j) && liveRoom.live[j.jobId]?.waiting);
  if (waiting.length === 0) return null;
  return (
    <div className="mt-1 flex flex-col gap-1 text-xs text-[var(--color-fg-subtle)]">
      {waiting.map((j) => {
        const live = liveRoom.live[j.jobId];
        return (
          <div key={j.jobId} className="flex flex-col gap-1 rounded-[var(--radius)] bg-[var(--color-surface-2)] px-2 py-1.5" data-live-waiting>
            <LiveWaitingNote roomId={liveRoom.roomId} live={live} subject={liveRoom.subject ?? null} onContinue={liveRoom.onContinue} onOpen={liveRoom.open ? () => liveRoom.open?.(j.jobId) : undefined} />
            <JobLiveThumb roomId={liveRoom.roomId} live={live} onOpen={liveRoom.open ? () => liveRoom.open?.(j.jobId) : undefined} />
          </div>
        );
      })}
    </div>
  );
}

const STEP_ICONS: Record<JobStep["kind"], typeof TerminalIcon> = {
  tool: TerminalIcon,
  thinking: BrainIcon,
  text: PencilSimpleLineIcon,
};

function StepLine({ step, current, lasted }: { step: JobStep; current?: boolean; lasted?: string | null }) {
  const Icon = STEP_ICONS[step.kind];
  const label = stepLabel(step);
  return (
    <span className={cn("flex min-w-0 items-center gap-1.5", current ? "text-[var(--color-fg-muted)]" : "text-[var(--color-fg-subtle)]")}>
      <Icon size={12} className="shrink-0" aria-hidden />
      <span className="min-w-0 truncate" title={label}>
        {label}
      </span>
      {lasted && (
        <span className="shrink-0 tabular-nums" aria-label="This step for">
          · {lasted}
        </span>
      )}
    </span>
  );
}

// JobProgressStrip sits under a live job's chip: how long it has worked, its
// current step, the steps before it (collapsed), and — once the turn has been
// quiet a while — a rotating plain note. It ticks once a second while the
// page is visible; it renders one line of fixed height for the current step
// or note so the feed never jumps.
export function JobProgressStrip({ job }: { job: AgentJob }) {
  const progress = useJobProgress(job.jobId);
  const liveRoom = useContext(JobLiveContext);
  const live = liveRoom?.live[job.jobId];
  const [, setTick] = useState(0);
  const [open, setOpen] = useState(false);
  useVisiblePoll(() => setTick((t) => t + 1), 1000);
  const now = Date.now();
  const step = currentStep(progress);
  const earlier = earlierSteps(progress);
  const note = quietNote(job, progress, now);
  const lasted = currentStepFor(progress, now);
  return (
    <div className="flex flex-col gap-0.5" data-job-progress>
      <div className="flex h-4 min-w-0 items-center gap-2">
        <span className="shrink-0 tabular-nums" aria-label="Working for">
          {formatElapsed(now - turnStart(job, progress))}
        </span>
        <span className="min-w-0 flex-1">
          {note ? (
            <span className="block truncate italic" aria-live="polite">
              {note}
            </span>
          ) : step ? (
            <StepLine step={step} current lasted={lasted} />
          ) : (
            <span className="block truncate">Starting…</span>
          )}
        </span>
        {earlier.length > 0 && (
          <button
            type="button"
            onClick={() => setOpen((o) => !o)}
            aria-expanded={open}
            className="flex shrink-0 items-center gap-0.5 rounded-[var(--radius)] px-1 hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)]"
          >
            {earlier.length} earlier
            <CaretDownIcon size={10} className={cn("transition-transform", open && "rotate-180")} aria-hidden />
          </button>
        )}
      </div>
      {liveRoom && live && (
        <JobLiveThumb roomId={liveRoom.roomId} live={live} onOpen={liveRoom.open ? () => liveRoom.open?.(job.jobId) : undefined} />
      )}
      {open && earlier.length > 0 && (
        <div className="flex flex-col gap-0.5 border-l border-[var(--color-border)] pl-2">
          {/* While a note shows, the current step joins the list. */}
          {note && step && <StepLine step={step} current lasted={lasted} />}
          {earlier.map((s, i) => (
            <StepLine key={`${s.atMs}-${i}`} step={s} />
          ))}
        </div>
      )}
    </div>
  );
}
