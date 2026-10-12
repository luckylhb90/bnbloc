import type { AgentJob } from "@/lib/api";

export const AGENT_JOB_STATUS_LABELS: Record<AgentJob["status"], string> = {
  queued: "waiting for bridge",
  claimed: "claimed by bridge",
  running: "running",
  reflowing: "collecting reply",
  failed: "failed",
  timed_out: "timed out",
  cancelled: "cancelled",
  replied: "replied",
} as const;

export const AGENT_JOB_FAILURE_LABELS: Record<string, string> = {
  no_bridge_online: "no bridge online",
  agent_not_advertised: "agent not available",
  borrow_not_authorized: "not authorized",
  core_unavailable: "core unavailable",
  runner_start_failed: "runner failed to start",
  runner_killed: "runner was killed",
  runner_failed: "runner failed",
  turn_timeout: "turn timed out",
  reply_timeout: "reply timed out",
  cancelled: "cancelled",
  budget_exhausted: "monthly budget reached",
  sandbox_required: "agent sandbox required",
} as const;

export const LIVE_AGENT_JOB_STATUSES = new Set<AgentJob["status"]>([
  "queued",
  "claimed",
  "running",
  "reflowing",
]);

// jobContentMissing reads the requirements a running job's content could
// not meet (the bridge's diagnostics.contentMissing: skill packs an expert
// requires, a room expert that is gone): the turn runs without them.
export function jobContentMissing(diagnostics: unknown): string[] {
  if (!diagnostics || typeof diagnostics !== "object") return [];
  const m = (diagnostics as Record<string, unknown>).contentMissing;
  return Array.isArray(m) ? m.filter((x): x is string => typeof x === "string" && x.trim() !== "") : [];
}

// jobWearing is what a running job wears, from its diagnostics: the
// chat's expert and the room's ("Reviewer (v1.2.0)").
export function jobWearing(diagnostics: unknown): string[] {
  if (!diagnostics || typeof diagnostics !== "object") return [];
  const d = diagnostics as Record<string, unknown>;
  return [d.roomExpert, d.expert].filter((x): x is string => typeof x === "string" && x !== "");
}

// agentJobStatusLabel is a job row's status. `ahead` is how many of the
// serving host's jobs are queued before this one (platform agents, PRD v2
// §3): a waiting job behind others says so instead of "waiting for bridge".
export function agentJobStatusLabel(job: AgentJob, ahead = 0): string {
  if (job.status === "queued" && !job.failureReason && ahead > 0) {
    return `Queued — ${ahead} ${ahead === 1 ? "request" : "requests"} ahead`;
  }
  const base = job.status === "queued" && job.failureReason ? "retrying" : AGENT_JOB_STATUS_LABELS[job.status];
  const failure = job.failureReason ? AGENT_JOB_FAILURE_LABELS[job.failureReason] ?? job.failureReason : "";
  return failure ? `${base}: ${failure}` : base;
}
