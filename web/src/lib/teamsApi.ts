import { req } from "./api.ts";
import type { TeamPlan, TeamPlanRequest } from "./teams.ts";

// Expert teams: which agent takes each role of a team in a room, and the
// pre-flight estimate (activity's POST /v1/rooms/{room}/team-plan).
export const teamApi = {
  plan: (roomId: string, body: TeamPlanRequest) =>
    req<TeamPlan>(`/v1/rooms/${encodeURIComponent(roomId)}/team-plan`, { method: "POST", body: JSON.stringify(body) }),
};
