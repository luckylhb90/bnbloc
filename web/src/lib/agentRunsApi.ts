import { req } from "./api.ts";
import { runsPath, toolUsePath, type AgentRun, type AgentRunsReply, type RoomToolUseReply, type RunListQuery } from "./agentRuns.ts";
import { roomPath } from "./roomId.ts";

// Agent run records (R10), served by activity. A room's runs need room
// membership; "self" is the caller's agents and requests; "tenant" is the
// whole organization (manage_org at the gateway).
export const agentRunsApi = {
  room: (roomId: string, q: RunListQuery = {}) => req<AgentRunsReply>(runsPath("room", q, roomId)),
  mine: (q: RunListQuery = {}) => req<AgentRunsReply>(runsPath("self", q)),
  tenant: (q: RunListQuery = {}) => req<AgentRunsReply>(runsPath("tenant", q)),
  // One room run with the first reply's room sequence; its raw diagnostics
  // only for the agent's owner and org admins (O4).
  detail: (roomId: string, jobId: string) =>
    req<AgentRun>(roomPath(roomId, "jobs", jobId)),
  // The organization's room tool calls, refusals and errors per tool over
  // the runs of the last `days` days (manage_org).
  toolUse: (days: number) => req<RoomToolUseReply>(toolUsePath(days, Date.now())),
};
