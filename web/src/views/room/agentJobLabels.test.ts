import { agentJobStatusLabel, failureLabel, failureWhy, retryNote, retryWaiting } from "./agentJobLabels.ts";
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
assert(retryNote(retried, 0) === "retrying — the agent sandbox was starting", `retry note = ${retryNote(retried, 0)}`);
assert(
  retryNote(job({ status: "running", attempt: 3, attempts: [starting, lapsed] }), 0) === "retrying — the agent's bridge stopped responding",
  "the newest attempt says why",
);
// Requeued and waiting for its next claim: queued, the reason recorded.
const requeued = job({ status: "queued", failureReason: "core_unavailable", attempts: [{ attempt: 1, reason: "core_unavailable", atMs: 3 }], notBeforeMs: 10_003 });
assert(agentJobStatusLabel(requeued, 2) === "waiting for bridge", "a requeued job is not 'Queued — N ahead'");
// Inside its retry delay the note counts down (whole seconds, rounded up).
assert(retryNote(requeued, 3) === "retrying in 10s — the agent's core was unreachable", `requeued note = ${retryNote(requeued, 3)}`);
assert(retryNote(requeued, 9_500) === "retrying in 1s — the agent's core was unreachable", `last second = ${retryNote(requeued, 9_500)}`);
assert(retryWaiting(requeued, 3) && !retryWaiting(requeued, 10_003), "waiting until notBeforeMs");
assert(retryNote(requeued, 10_003) === "retrying — the agent's core was unreachable", `delay over = ${retryNote(requeued, 10_003)}`);
assert(retryNote({ ...retried, notBeforeMs: 99_999 }, 0) === "retrying — the agent sandbox was starting", "a claimed retry no longer counts down");
assert(retryNote(job({ status: "running" }), 0) === "", "a first attempt has no retry note");
assert(retryNote(job({ status: "failed", attempt: 2, attempts: [starting] }), 0) === "", "a finished job has no retry note");
// A reason this bundle does not know reads as words, never as a blank.
assert(failureWhy("brand_new_reason") === "brand new reason", "unknown reason fallback");
assert(failureLabel("sandbox_starting") === "sandbox starting", "label");
