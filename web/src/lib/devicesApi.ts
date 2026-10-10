import { req } from "./api.ts";
import type { MySession } from "./devices.ts";

export const devicesApi = {
  // The caller's active sign-ins; `current` marks the one this page runs in.
  list: () => req<{ sessions: MySession[] }>("/v1/my/sessions"),
  // Revokes one sign-in and the agent sessions forked from it.
  revoke: (id: string) => req<void>(`/v1/my/sessions/${encodeURIComponent(id)}`, { method: "DELETE", raw: true }),
  // Revokes every sign-in except this one and its own agent.
  revokeOthers: () => req<{ revoked: number }>("/v1/my/sessions?others=1", { method: "DELETE" }),
};
