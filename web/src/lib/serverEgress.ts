import { req } from "./api.ts";
import type { ServerEgress } from "./wire.gen.ts";

// The platform console's Network card: where the platform's own
// server-side requests may go (docs/architecture/server-egress.md).

export type { ServerEgress } from "./wire.gen.ts";

export const serverEgressApi = {
  get: () => req<ServerEgress>("/v1/platform/server-egress"),
  put: (body: Pick<ServerEgress, "mode" | "hosts">) => req<ServerEgress>("/v1/platform/server-egress", { method: "PUT", body: JSON.stringify(body) }),
};
