import { req } from "./api.ts";
import { runsPath, type AgentRun, type AgentRunsReply, type RunListQuery } from "./agentRuns.ts";
import { roomPath } from "./roomId.ts";

// Agent run records (R10), served by activity. A room's runs need room
// membership; "self" is the caller's agents and requests; "tenant" is the
// whole organization (manage_org at the gateway).
export const agentRunsApi = {
  room: (roomId: string, q: RunListQuery = {}) => req<AgentRunsReply>(runsPath("room", q, roomId)),
  mine: (q: RunListQuery = {}) => req<AgentRunsReply>(runsPath("self", q)),
  tenant: (q: RunListQuery = {}) => req<AgentRunsReply>(runsPath("tenant", q)),
  // One room run with its diagnostics and the first reply's room sequence.
  detail: (roomId: string, jobId: string) =>
    req<AgentRun>(roomPath(roomId, "jobs", jobId)),
};
