import { agentJobStatusLabel } from "./agentJobLabels.ts";
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
assert(agentJobStatusLabel(job({ failureReason: "no_bridge_online" }), 3) === "retrying: no bridge online", "a retry keeps its reason");
