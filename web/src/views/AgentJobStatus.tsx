import type { AgentJob } from "@/lib/api";
import { cn } from "@/lib/cn";
import { AgentHandle } from "@/views/UserName";
import { agentJobStatusLabel, LIVE_AGENT_JOB_STATUSES } from "@/views/room/agentJobLabels";

// failureDetail pulls the bridge's actionable error out of the job diagnostics.
// The reason code says which stage failed; this says why, which is what a member
// needs to act on (missing key, disconnected runtime).
function failureDetail(diagnostics: unknown): string {
  if (!diagnostics || typeof diagnostics !== "object") return "";
  const d = diagnostics as Record<string, unknown>;
  const detail = d.error ?? d.warning;
  return typeof detail === "string" ? detail : "";
}

// diagnosticRows flattens the bridge's diagnostics for the developer panel.
// Values are stringified defensively: diagnostics are free-form JSON from the
// worker, not a fixed shape.
function diagnosticRows(diagnostics: unknown): [string, string][] {
  if (!diagnostics || typeof diagnostics !== "object") return [];
  return Object.entries(diagnostics as Record<string, unknown>).map(([k, v]) => [
    k,
    typeof v === "string" ? v : JSON.stringify(v),
  ]);
}

export function AgentJobStatus({
  jobs,
  showDiagnostics = false,
  onCancel,
  queueAhead,
}: {
  jobs: AgentJob[];
  showDiagnostics?: boolean;
  onCancel?: (jobId: string) => void;
  // Jobs queued before this one on its host (platform agents).
  queueAhead?: (job: AgentJob) => number;
}) {
  const visible = jobs.filter((j) => j.status !== "replied");
  if (visible.length === 0) return null;
  return (
    <div className="mt-1 flex flex-col gap-1 text-xs text-[var(--color-fg-subtle)]">
      {visible.map((j) => {
        const failed = j.status === "failed" || j.status === "timed_out" || j.status === "cancelled";
        const detail = failed ? failureDetail(j.diagnostics) : "";
        const live = LIVE_AGENT_JOB_STATUSES.has(j.status);
        const rows = showDiagnostics ? diagnosticRows(j.diagnostics) : [];
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
                {agentJobStatusLabel(j, queueAhead?.(j))}
              </span>
              {j.attempt > 1 && <span>attempt {j.attempt}</span>}
              {live && onCancel && (
                <button
                  type="button"
                  onClick={() => onCancel(j.jobId)}
                  className="ml-auto rounded-[var(--radius)] px-1.5 py-0.5 text-[var(--color-fg-muted)] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)]"
                >
                  stop
                </button>
              )}
            </div>
            {detail && <span className="break-words text-[var(--color-fg-subtle)]">{detail}</span>}
            {showDiagnostics && (
              <div className="mt-0.5 flex flex-col gap-0.5 border-t border-[var(--color-border)] pt-1 font-mono text-[10px] text-[var(--color-fg-subtle)]">
                <span>job {j.jobId}</span>
                {j.leaseBridgeId && <span>bridge {j.leaseBridgeId}</span>}
                <span>
                  attempt {j.attempt}
                  {j.leaseExpiresAtMs ? ` · lease ${new Date(j.leaseExpiresAtMs).toLocaleTimeString()}` : ""}
                </span>
                {rows.map(([k, v]) => (
                  <span key={k} className="break-all">
                    {k} {v}
                  </span>
                ))}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
