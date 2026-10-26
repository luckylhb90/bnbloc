import type { AgentJob } from "@/lib/api";
import type { AgentJobFailureReason, AgentJobStatus } from "@/lib/wire.gen";
import { isLiveJob } from "../../lib/agentRuns.ts";

export const AGENT_JOB_STATUS_LABELS: Record<AgentJobStatus, string> = {
  queued: "waiting for bridge",
  claimed: "claimed by bridge",
  running: "running",
  reflowing: "working",
  failed: "failed",
  timed_out: "timed out",
  cancelled: "cancelled",
  replied: "replied",
} as const;

// AGENT_JOB_FAILURES is the web's one reason table: each failure reason's
// short label (a status chip, a run row: "failed: core unavailable") and
// why it happened, as a clause ("retrying — the agent's core was
// unreachable"). A failed job's notice sentence is the server's
// (store.AgentJobFailureReason.Text).
export const AGENT_JOB_FAILURES: Record<AgentJobFailureReason, { readonly label: string; readonly why: string }> = {
  no_bridge_online: { label: "no bridge online", why: "no bridge was online" },
  agent_not_advertised: { label: "agent not available", why: "the agent was not available" },
  borrow_not_authorized: { label: "not authorized", why: "the borrow was not authorized" },
  core_unavailable: { label: "core unavailable", why: "the agent's core was unreachable" },
  runner_start_failed: { label: "runner failed to start", why: "the agent's runner did not start" },
  runner_failed: { label: "runner failed", why: "the agent's runner failed" },
  agent_error: { label: "agent error", why: "the agent reported an error" },
  context_overflow: { label: "context window exceeded", why: "the conversation outgrew the model's context window" },
  context_reset: { label: "conversation reset", why: "the conversation outgrew the model's context window while the agent worked; send the request again" },
  lease_expired: { label: "bridge stopped responding", why: "the agent's bridge stopped responding" },
  sandbox_starting: { label: "sandbox starting", why: "the agent sandbox was starting" },
  reply_timeout: { label: "ran out of time", why: "it ran out of time" },
  no_reply: { label: "no reply", why: "it finished without a visible reply" },
  cancelled: { label: "cancelled", why: "it was cancelled" },
  budget_exhausted: { label: "monthly budget reached", why: "a monthly budget was reached" },
  budget_unavailable: { label: "budget could not be checked", why: "the budget could not be checked" },
  sandbox_required: { label: "agent sandbox required", why: "an agent sandbox was required" },
  content_unavailable: { label: "expert unavailable", why: "the expert was unavailable" },
  team_unsupported: { label: "team needs the room tools", why: "the team needs the room tools" },
  invalid_job: { label: "nothing to run", why: "there was nothing to run" },
} as const;

type FailureEntry = (typeof AGENT_JOB_FAILURES)[AgentJobFailureReason];

// failureEntry is a reason's row; undefined for a reason this bundle does
// not know yet (a newer server), which then reads as its code in words.
function failureEntry(reason: string): FailureEntry | undefined {
  return (AGENT_JOB_FAILURES as Record<string, FailureEntry | undefined>)[reason];
}

// failureLabel is a reason's short label.
export function failureLabel(reason: string): string {
  return failureEntry(reason)?.label ?? reason.replace(/_/g, " ");
}

// failureWhy is a reason's "why" clause.
export function failureWhy(reason: string): string {
  return failureEntry(reason)?.why ?? reason.replace(/_/g, " ");
}

// jobWearing is what a job wears: the room's expert and the chat's own
// ("Reviewer (v1.2.0)").
export function jobWearing(job: Pick<AgentJob, "roomExpertName" | "roomExpertVersion" | "expertName" | "expertVersion">): string[] {
  const label = (name?: string, version?: string) => (name ? (version ? `${name} (v${version})` : name) : "");
  return [label(job.roomExpertName, job.roomExpertVersion), label(job.expertName, job.expertVersion)].filter((x) => x !== "");
}

// retryNote says why a live job is being retried: its last failed attempt's
// reason ("retrying — the agent sandbox was starting"), and while it waits
// out its retry delay, when ("retrying in 12s — …"). Empty for a job that
// never failed an attempt, and for a finished job (its outcome says the
// rest).
export function retryNote(job: Pick<AgentJob, "status" | "attempts" | "notBeforeMs">, nowMs: number): string {
  const last = job.attempts?.at(-1);
  if (!last || !isLiveJob(job)) return "";
  const waitS = job.status === "queued" ? Math.ceil(((job.notBeforeMs ?? 0) - nowMs) / 1000) : 0;
  return `retrying${waitS > 0 ? ` in ${waitS}s` : ""} — ${failureWhy(last.reason)}`;
}

// retryWaiting reports a job still inside its retry delay at nowMs (the
// indicator's countdown ticks only then).
export function retryWaiting(job: Pick<AgentJob, "status" | "notBeforeMs">, nowMs: number): boolean {
  return job.status === "queued" && (job.notBeforeMs ?? 0) > nowMs;
}

// agentJobStatusLabel is a job row's status. `ahead` is how many of the
// serving host's jobs are queued before this one (platform agents, PRD v2
// §3): a waiting job behind others says so instead of "waiting for bridge".
// A finished job names its failure; a live one's reason is an earlier
// attempt's, which retryNote says.
export function agentJobStatusLabel(job: AgentJob, ahead = 0): string {
  if (job.status === "queued" && !job.attempts?.length && ahead > 0) {
    return `Queued — ${ahead} ${ahead === 1 ? "request" : "requests"} ahead`;
  }
  const base = AGENT_JOB_STATUS_LABELS[job.status];
  return job.failureReason && !isLiveJob(job) ? `${base}: ${failureLabel(job.failureReason)}` : base;
}

// steeredNote is what a job's row says when its message joined the turn the
// agent was already running (docs/prd/2026-10-25-steer-a-running-turn.md):
// the job asked nothing of its own, and the running one's reply answers both.
export function steeredNote(job: Pick<AgentJob, "steeredInto">): string {
  return job.steeredInto ? "joined the running task" : "";
}

// queuedBehind is the earlier turn a job waits for: a live job of the same
// session (same person, channel and agent: the turns of one session queue)
// whose turn is under way ("reflowing") while this one, reported running, has
// not started its own (it waits at the session's gate). The member
// may stop it and have this one start now; nothing stops by itself.
// decision: steer PRD §5.4 (a request queued behind its own session's running turn; stops that job by id).
export function queuedBehind(job: AgentJob, all: readonly AgentJob[]): AgentJob | undefined {
  if (job.status !== "running" || job.steeredInto) return undefined;
  return all.find(
    (o) =>
      o.jobId !== job.jobId &&
      o.status === "reflowing" &&
      o.messageSeq < job.messageSeq &&
      o.requesterUserId === job.requesterUserId &&
      o.channelId === job.channelId &&
      o.ownerUserId === job.ownerUserId &&
      o.core === job.core &&
      o.provider === job.provider,
  );
}
