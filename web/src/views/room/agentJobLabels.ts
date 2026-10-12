import type { AgentJob } from "@/lib/api";
import type { AgentJobFailureReason, AgentJobStatus } from "@/lib/wire.gen";

export const AGENT_JOB_STATUS_LABELS: Record<AgentJobStatus, string> = {
  queued: "waiting for bridge",
  claimed: "claimed by bridge",
  running: "running",
  reflowing: "collecting reply",
  failed: "failed",
  timed_out: "timed out",
  cancelled: "cancelled",
  replied: "replied",
} as const;

export const AGENT_JOB_FAILURE_LABELS: Record<AgentJobFailureReason, string> = {
  no_bridge_online: "no bridge online",
  agent_not_advertised: "agent not available",
  borrow_not_authorized: "not authorized",
  core_unavailable: "core unavailable",
  runner_start_failed: "runner failed to start",
  runner_failed: "runner failed",
  turn_timeout: "turn timed out",
  reply_timeout: "reply timed out",
  cancelled: "cancelled",
  budget_exhausted: "monthly budget reached",
  budget_unavailable: "budget could not be checked",
  sandbox_required: "agent sandbox required",
  content_unavailable: "expert unavailable",
  team_unsupported: "team needs the room tools",
  invalid_job: "nothing to run",
} as const;

// jobWearing is what a job wears: the room's expert and the chat's own
// ("Reviewer (v1.2.0)").
export function jobWearing(job: Pick<AgentJob, "roomExpertName" | "roomExpertVersion" | "expertName" | "expertVersion">): string[] {
  const label = (name?: string, version?: string) => (name ? (version ? `${name} (v${version})` : name) : "");
  return [label(job.roomExpertName, job.roomExpertVersion), label(job.expertName, job.expertVersion)].filter((x) => x !== "");
}

// agentJobStatusLabel is a job row's status. `ahead` is how many of the
// serving host's jobs are queued before this one (platform agents, PRD v2
// §3): a waiting job behind others says so instead of "waiting for bridge".
export function agentJobStatusLabel(job: AgentJob, ahead = 0): string {
  if (job.status === "queued" && !job.failureReason && ahead > 0) {
    return `Queued — ${ahead} ${ahead === 1 ? "request" : "requests"} ahead`;
  }
  const base = job.status === "queued" && job.failureReason ? "retrying" : AGENT_JOB_STATUS_LABELS[job.status];
  const failure = job.failureReason ? AGENT_JOB_FAILURE_LABELS[job.failureReason] : "";
  return failure ? `${base}: ${failure}` : base;
}
