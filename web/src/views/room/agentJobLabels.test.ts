import { agentJobStatusLabel, failureLabel, failureWhy, retryNote } from "./agentJobLabels.ts";
import type { AgentJob } from "../../lib/apiModels.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const job = (over: Partial<AgentJob>): AgentJob => ({
  jobId: "j",
  roomId: "r",
  channelId: "",
  messageSeq: 1,
  requesterUserId: "jack",
  ownerUserId: "admin",
  core: "nimbus",
  provider: "codex",
  messageText: "",
  status: "queued",
  attempt: 1,
  createdAtMs: 1,
  updatedAtMs: 1,
  ...over,
});

assert(agentJobStatusLabel(job({})) === "waiting for bridge", "nothing ahead: the plain label");
assert(agentJobStatusLabel(job({}), 1) === "Queued — 1 request ahead", "singular");
assert(agentJobStatusLabel(job({}), 3) === "Queued — 3 requests ahead", "plural");
assert(agentJobStatusLabel(job({ status: "running" }), 3) === "running", "a started job is not queued");
assert(agentJobStatusLabel(job({ status: "failed", failureReason: "no_bridge_online" })) === "failed: no bridge online", "a finished job names its failure");

// A retried job says why while it is live: the indicator's retry note is the
// last failed attempt's reason, from the one reason table.
const starting = { attempt: 1, reason: "sandbox_starting" as const, atMs: 5 };
const lapsed = { attempt: 2, reason: "lease_expired" as const, atMs: 9 };
const retried = job({ status: "reflowing", attempt: 2, attempts: [starting] });
assert(agentJobStatusLabel(retried) === "collecting reply", "the phase, not the earlier attempt's reason, labels a live job");
assert(retryNote(retried) === "retrying — the agent sandbox was starting", `retry note = ${retryNote(retried)}`);
assert(
  retryNote(job({ status: "running", attempt: 3, attempts: [starting, lapsed] })) === "retrying — the agent's bridge stopped responding",
  "the newest attempt says why",
);
// Requeued and waiting for its next claim: queued, the reason recorded.
const requeued = job({ status: "queued", failureReason: "core_unavailable", attempts: [{ attempt: 1, reason: "core_unavailable", atMs: 3 }] });
assert(agentJobStatusLabel(requeued, 2) === "waiting for bridge", "a requeued job is not 'Queued — N ahead'");
assert(retryNote(requeued) === "retrying — the agent's core was unreachable", `requeued note = ${retryNote(requeued)}`);
assert(retryNote(job({ status: "running" })) === "", "a first attempt has no retry note");
assert(retryNote(job({ status: "failed", attempt: 2, attempts: [starting] })) === "", "a finished job has no retry note");
// A reason this bundle does not know reads as words, never as a blank.
assert(failureWhy("brand_new_reason") === "brand new reason", "unknown reason fallback");
assert(failureLabel("sandbox_starting") === "sandbox starting", "label");
