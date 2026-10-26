import type { AgentJob } from "@/lib/api";
import { cn } from "@/lib/cn";
import { AgentHandle } from "@/views/UserName";
import { isLiveJob } from "@/lib/agentRuns";
import { agentJobStatusLabel, jobWearing, retryNote, steeredNote } from "@/views/room/agentJobLabels";
import { useRetryClock } from "@/views/room/useRetryClock";
import { JobProgressStrip } from "@/views/room/JobProgressStrip";

// The job's raw diagnostics stay with its owner and admins (the run record);
// a row shows the projection's failure text, what the turn wore and what it
// runs without.
export function AgentJobStatus({
  jobs,
  onCancel,
  queueAhead,
  behind,
}: {
  jobs: AgentJob[];
  onCancel?: (jobId: string) => void;
  // Jobs queued before this one on its host (platform agents).
  queueAhead?: (job: AgentJob) => number;
  // The earlier turn a job waits for (queuedBehind): its "stop it and do this
  // now" button stops that one, by the member's click.
  behind?: (job: AgentJob) => AgentJob | undefined;
}) {
  // A replied job has nothing to show, except one whose message joined a running turn.
  const visible = jobs.filter((j) => j.status !== "replied" || steeredNote(j));
  const now = useRetryClock(visible);
  if (visible.length === 0) return null;
  return (
    <div className="mt-1 flex flex-col gap-1 text-xs text-[var(--color-fg-subtle)]">
      {visible.map((j) => {
        const failed = j.status === "failed" || j.status === "timed_out" || j.status === "cancelled";
        // The reason code says which stage failed; this says why, which is
        // what a member acts on (missing key, disconnected runtime).
        const detail = failed ? j.error || j.warning || "" : "";
        const live = isLiveJob(j);
        const wearing = live ? jobWearing(j) : [];
        const missing = live ? (j.contentMissing ?? []) : [];
        const retry = retryNote(j, now);
        const ahead = live && onCancel ? behind?.(j) : undefined;
        return (
          <div
            key={j.jobId}
            className="flex flex-col gap-0.5 rounded-[var(--radius)] bg-[var(--color-surface-2)] px-2 py-1"
          >
            <div className="flex items-center gap-2">
              {/* The agent as members know it ("<owner name> · <provider>"):
                  never the raw core/provider pair, which names deployment
                  internals (the core) and an e-mail id. */}
              <AgentHandle owner={j.ownerUserId} provider={j.provider} className="font-medium text-[var(--color-fg-muted)]" />
              <span
                className={cn(
                  "rounded-full px-1.5 py-0.5",
                  failed ? "bg-[var(--color-danger,#7f1d1d)] text-white" : "bg-[var(--color-surface)]",
                )}
              >
                {steeredNote(j) || agentJobStatusLabel(j, queueAhead?.(j))}
              </span>
              {j.attempt > 1 && <span>attempt {j.attempt}</span>}
              {retry && <span className="min-w-0 truncate" title={retry}>{retry}</span>}
              {ahead && onCancel && (
                <button
                  type="button"
                  data-stop-and-send
                  onClick={() => onCancel(ahead.jobId)}
                  className="ml-auto rounded-[var(--radius)] px-1.5 py-0.5 text-[var(--color-fg-muted)] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)]"
                  title="Stops the task that is running now (what it has not finished is lost); this request then starts"
                >
                  Stop the current task and do this now
                </button>
              )}
              {live && onCancel && (
                <button
                  type="button"
                  onClick={() => onCancel(j.jobId)}
                  className={cn("rounded-[var(--radius)] px-1.5 py-0.5 text-[var(--color-fg-muted)] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)]", !ahead && "ml-auto")}
                >
                  stop
                </button>
              )}
            </div>
            {/* A started job shows what it is doing (live-turns PRD §3). */}
            {live && j.status !== "queued" && <JobProgressStrip job={j} />}
            {detail && <span className="break-words text-[var(--color-fg-subtle)]">{detail}</span>}
            {wearing.length > 0 && <span className="break-words">as {wearing.join(" + ")}</span>}
            {missing.length > 0 && (
              <span data-content-missing className="break-words text-[var(--color-warning)]" title="Not available to the person who asked: the agent runs without it">
                Running without {missing.join(", ")}
              </span>
            )}
          </div>
        );
      })}
    </div>
  );
}
