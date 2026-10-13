import { createContext, useContext, useState } from "react";
import { BrainIcon, CaretDownIcon, PencilSimpleLineIcon, TerminalIcon } from "@phosphor-icons/react";
import type { AgentJob } from "@/lib/api";
import { cn } from "@/lib/cn";
import {
  currentStep,
  earlierSteps,
  formatElapsed,
  quietNote,
  stepLabel,
  turnStart,
  type JobProgress,
  type JobStep,
} from "@/lib/jobProgress";
import { useVisiblePoll } from "@/lib/useVisiblePoll";

// A live job's progress (live-turns PRD §3) reaches the job rows through
// context, so the feed's row props stay as they are: the room's map by job
// id (useRoomActivity owns it), empty outside a room.
const JobProgressContext = createContext<Readonly<Record<string, JobProgress>>>({});
export const JobProgressProvider = JobProgressContext.Provider;

const STEP_ICONS: Record<JobStep["kind"], typeof TerminalIcon> = {
  tool: TerminalIcon,
  thinking: BrainIcon,
  text: PencilSimpleLineIcon,
};

function StepLine({ step, current }: { step: JobStep; current?: boolean }) {
  const Icon = STEP_ICONS[step.kind];
  const label = stepLabel(step);
  return (
    <span className={cn("flex min-w-0 items-center gap-1.5", current ? "text-[var(--color-fg-muted)]" : "text-[var(--color-fg-subtle)]")}>
      <Icon size={12} className="shrink-0" aria-hidden />
      <span className="min-w-0 truncate" title={label}>
        {label}
      </span>
    </span>
  );
}

// JobProgressStrip sits under a live job's chip: how long it has worked, its
// current step, the steps before it (collapsed), and — once the turn has been
// quiet a while — a rotating plain note. It ticks once a second while the
// page is visible; it renders one line of fixed height for the current step
// or note so the feed never jumps.
export function JobProgressStrip({ job }: { job: AgentJob }) {
  const progress = useContext(JobProgressContext)[job.jobId];
  const [, setTick] = useState(0);
  const [open, setOpen] = useState(false);
  useVisiblePoll(() => setTick((t) => t + 1), 1000);
  const now = Date.now();
  const step = currentStep(progress);
  const earlier = earlierSteps(progress);
  const note = quietNote(job, progress, now);
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
            <StepLine step={step} current />
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
      {open && earlier.length > 0 && (
        <div className="flex flex-col gap-0.5 border-l border-[var(--color-border)] pl-2">
          {/* While a note shows, the current step joins the list. */}
          {note && step && <StepLine step={step} current />}
          {earlier.map((s, i) => (
            <StepLine key={`${s.atMs}-${i}`} step={s} />
          ))}
        </div>
      )}
    </div>
  );
}
