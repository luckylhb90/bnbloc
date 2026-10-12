import { req } from "./api.ts";
import type {
  PlatformAgent,
  PlatformAgentRef,
  PlatformAgentsPolicy,
  PlatformAgentsPolicyInput,
  RoomPlatformAgent,
} from "./platformAgents.ts";
import { normalizeUsage, usageQuery, type PlatformAgentsUsage } from "./platformUsage.ts";

// Go encodes an empty list as null.
const normalizedPolicy = (p: PlatformAgentsPolicy): PlatformAgentsPolicy => ({
  hosts: (p.hosts ?? []).map((h) => ({ ...h, agents: h.agents ?? [] })),
});

function normalizedAgents<T extends PlatformAgent>(res: { agents: T[] | null }): { agents: T[] } {
  return { agents: (res.agents ?? []).map((a) => ({ ...a, hosts: a.hosts ?? [] })) };
}

const roomPath = (roomId: string) => `/v1/rooms/${encodeURIComponent(roomId)}/platform-agents`;

export const platformAgentsApi = {
  // Super admin: every host's published set.
  policy: () => req<PlatformAgentsPolicy>("/v1/platform/platform-agents").then(normalizedPolicy),
  // Super admin: publish from the caller's machine (writes the caller's entry
  // only; other hosts are untouched). Answers the whole policy.
  putPolicy: (body: PlatformAgentsPolicyInput) =>
    req<PlatformAgentsPolicy>("/v1/platform/platform-agents", { method: "PUT", body: JSON.stringify(body) }).then(
      normalizedPolicy,
    ),
  // Super admin: today/month totals, top members and rooms, recent runs.
  usage: () => req<PlatformAgentsUsage>(`/v1/platform/platform-agents/usage?${usageQuery()}`).then(normalizeUsage),
  // The agents offered to the caller's organization (empty when off), with
  // each host's online/queued state.
  available: () => req<{ agents: PlatformAgent[] | null }>("/v1/platform-agents").then(normalizedAgents),
  // The room's added platform agents (room members).
  inRoom: (roomId: string) => req<{ agents: RoomPlatformAgent[] | null }>(roomPath(roomId)).then(normalizedAgents),
  // Idempotent: grants the room a standing authorization for the agent on
  // every host that publishes it.
  add: (roomId: string, agent: PlatformAgentRef) =>
    req<{ agent: RoomPlatformAgent }>(roomPath(roomId), {
      method: "POST",
      body: JSON.stringify({ core: agent.core, provider: agent.provider }),
    }),
  remove: (roomId: string, agent: PlatformAgentRef) =>
    req<void>(`${roomPath(roomId)}/${encodeURIComponent(agent.provider)}?core=${encodeURIComponent(agent.core)}`, {
      method: "DELETE",
      as: "none",
    }),
};
