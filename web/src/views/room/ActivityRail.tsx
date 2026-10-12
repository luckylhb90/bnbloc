import { CaretDownIcon, FileDocIcon, XIcon } from "@phosphor-icons/react";
import type { AgentJob } from "@/lib/api";
import { cn } from "@/lib/cn";
import { relativeTime } from "@/lib/format";
import { ActorLabel } from "@/views/room/ActorLabel";
import { isLiveJob } from "@/lib/agentRuns";
import { agentJobStatusLabel } from "@/views/room/agentJobLabels";
import { agentHandle } from "@/lib/roomAgents";

export type RecentArtifactChange = {
  readonly artifactId: string;
  readonly title: string;
  readonly currentVersion: number;
  readonly updatedBy: string;
  readonly updatedAtMs: number;
};

type WorkingAgent = {
  readonly key: string;
  readonly label: string;
};

function railJobTone(job: AgentJob): string {
  switch (job.status) {
    case "queued":
    case "claimed":
      return "border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 text-[var(--color-warning)]";
    case "running":
    case "reflowing":
      return "border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] text-[var(--color-accent)]";
    case "failed":
    case "timed_out":
    case "cancelled":
      return "border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 text-[var(--color-danger)]";
    case "replied":
      return "border-[var(--color-border)] bg-[var(--color-surface-2)] text-[var(--color-fg-subtle)]";
  }
}

export function ActivityRail({
  working,
  agentJobs,
  recentArtifactChanges,
  onCancelJob,
  queueAhead,
}: {
  readonly working: Map<string, string>;
  readonly agentJobs: Record<number, AgentJob[]>;
  readonly recentArtifactChanges: readonly RecentArtifactChange[];
  readonly onCancelJob: (jobId: string) => void;
  // Jobs queued before a job on its platform host ("Queued — N ahead").
  readonly queueAhead?: (job: AgentJob) => number;
}) {
  const workingAgents: readonly WorkingAgent[] = [...working].map(([key, label]) => ({ key, label }));
  const visibleJobs = Object.values(agentJobs)
    .flat()
    .filter((job) => job.status !== "replied");
  const activeCount = workingAgents.length + visibleJobs.length + recentArtifactChanges.length;

  if (activeCount === 0) {
    return (
      <div className="border-b border-[var(--color-border)] bg-[var(--color-bg)] px-4 py-1.5 text-[11px] text-[var(--color-fg-subtle)]">
        Room activity quiet
      </div>
    );
  }

  return (
    <details className="group border-b border-[var(--color-border)] bg-[var(--color-surface)] open:bg-[var(--color-surface-2)]">
      <summary className="tactile flex cursor-pointer list-none items-center gap-2 px-4 py-2 text-xs text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] [&::-webkit-details-marker]:hidden">
        <span className="flex items-center gap-1.5 font-medium text-[var(--color-fg)]">
          <span className="inline-block size-1.5 rounded-full bg-[var(--color-accent)] pulse-dot" />
          Activity
        </span>
        <span className="truncate">
          {workingAgents.length > 0 ? (
            <>
              <ActorLabel actor={workingAgents[0]?.label ?? "agent"} className="bg-transparent px-0 py-0" /> working
            </>
          ) : visibleJobs.length > 0
            ? agentJobStatusLabel(visibleJobs[0], queueAhead?.(visibleJobs[0]))
            : `${recentArtifactChanges[0]?.title ?? "Artifact"} updated`}
        </span>
        {activeCount > 1 && (
          <span className="rounded-full border border-[var(--color-border)] px-1.5 text-[10px] tabular text-[var(--color-fg-subtle)]">
            +{activeCount - 1}
          </span>
        )}
        <CaretDownIcon size={12} className="ml-auto shrink-0 transition-transform group-open:rotate-180" />
      </summary>
      <div className="flex flex-wrap gap-2 px-4 pb-2 text-xs">
        {workingAgents.map((agent) => (
          <span
            key={agent.key}
            className="inline-flex max-w-full items-center gap-1.5 rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] px-2 py-1 text-[var(--color-accent)]"
          >
            <ActorLabel actor={agent.label} />
            <span className="shrink-0 text-[var(--color-fg-muted)]">working</span>
          </span>
        ))}
        {visibleJobs.map((job) => {
          const live = isLiveJob(job);
          return (
            <span
              key={job.jobId}
              className={cn("inline-flex max-w-full items-center gap-1.5 rounded-full border px-2 py-1", railJobTone(job))}
            >
              <ActorLabel actor={agentHandle({ ownerId: job.ownerUserId, core: job.core || undefined, provider: job.provider })} />
              <span className="shrink-0">{agentJobStatusLabel(job, queueAhead?.(job))}</span>
              {job.attempt > 1 && <span className="shrink-0 text-[var(--color-fg-subtle)]">attempt {job.attempt}</span>}
              {live && (
                <button
                  type="button"
                  onClick={() => onCancelJob(job.jobId)}
                  className="tactile -mr-1 grid size-5 shrink-0 place-items-center rounded-full text-[var(--color-fg-muted)] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
                  title="Stop job"
                  aria-label={`Stop ${job.provider} job`}
                >
                  <XIcon size={12} />
                </button>
              )}
            </span>
          );
        })}
        {recentArtifactChanges.map((change) => (
          <span
            key={`${change.artifactId}:${change.currentVersion}`}
            className="inline-flex max-w-full items-center gap-1.5 rounded-full border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1 text-[var(--color-fg-muted)]"
          >
            <FileDocIcon size={12} className="shrink-0 text-[var(--color-accent)]" />
            <span className="min-w-0 truncate font-medium text-[var(--color-fg)]">{change.title}</span>
            <span className="shrink-0 tabular text-[var(--color-fg-subtle)]">v{change.currentVersion}</span>
            <span className="shrink-0 text-[var(--color-fg-subtle)]">by</span>
            <ActorLabel actor={change.updatedBy} />
            <span className="shrink-0 text-[var(--color-fg-subtle)]">{relativeTime(change.updatedAtMs)}</span>
          </span>
        ))}
      </div>
    </details>
  );
}
