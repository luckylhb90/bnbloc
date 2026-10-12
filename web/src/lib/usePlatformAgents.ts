import { useAuth } from "./auth";
import type { PlatformAgent } from "./platformAgents.ts";
import { platformAgentsApi } from "./platformAgentsApi.ts";
import { queryKey, useQuery } from "./useQuery.ts";

const STALE_MS = 30_000;
const NONE: readonly PlatformAgent[] = [];

// usePlatformAgents is the platform agents offered to the session's
// organization, shared by the composer picker and the zero-core guidance (one
// request per tenant, revalidated every 30 s for the online dots). A failed
// read offers none: the guidance simply does not mention them.
export function usePlatformAgents(): { readonly agents: readonly PlatformAgent[]; readonly refetch: () => void } {
  const { subject, tenant } = useAuth();
  const query = useQuery(
    queryKey("platform-agents", { subject, tenant }),
    platformAgentsApi.available,
    { staleMs: STALE_MS },
  );
  return { agents: query.data?.agents ?? NONE, refetch: query.refetch };
}
