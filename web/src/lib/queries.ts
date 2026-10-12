// Shared server-state queries (web M7, M8, M10): each named list is read
// through one cached query per member, tenant and gateway (useQuery's
// queryKey), so every surface shows the same answer and a write refreshes
// them all at once (invalidateQueries) instead of each reader polling its
// own copy.
import { api } from "./api";
import { useAuth } from "./auth";
import { invalidateQueries, queryKey, useQuery, type QueryResult } from "./useQuery";
import type { PlatformTenant } from "./api";
import type { MyAgentsReply, QuotaRequest } from "./wire.gen.ts";

const MY_AGENTS = "my-agents";
const TENANTS = "platform-tenants";

// useMyAgents is GET /v1/my/agents: the member's own advertised agents and
// their desktop app's connection. Fresh for 30 s; refreshMyAgents after a
// change (an agent registered, a bridge started).
export function useMyAgents(enabled = true): QueryResult<MyAgentsReply> {
  const { subject, tenant } = useAuth();
  return useQuery(enabled ? queryKey(MY_AGENTS, { subject, tenant }) : null, api.myAgents, { staleMs: 30_000 });
}

export function refreshMyAgents(): void {
  invalidateQueries(MY_AGENTS);
}

// useTenants is the platform's tenant list (super admins): the acting-tenant
// switcher, the console and organization sync read the one copy.
// refreshTenants after creating or changing a tenant.
export function useTenants(enabled = true): QueryResult<PlatformTenant[]> {
  const { subject, tenant } = useAuth();
  return useQuery(enabled ? queryKey(TENANTS, { subject, tenant }) : null, async () => (await api.listTenants()).tenants, { staleMs: 60_000 });
}

export function refreshTenants(): void {
  invalidateQueries(TENANTS);
}

const QUOTA_REQUESTS = "quota-requests";

// useQuotaRequests is one side of the borrow quota requests (owner: those
// addressed to me; requester: mine), optionally pending only and in one
// room. The authorization badge and the panel in the same dialog read the
// same query instead of polling it twice. refreshQuotaRequests after a
// decision or a new request.
export function useQuotaRequests(role: "owner" | "requester", opts: { readonly roomId?: string; readonly pendingOnly?: boolean } = {}): QueryResult<QuotaRequest[]> {
  const { subject, tenant } = useAuth();
  const status = opts.pendingOnly ? "pending" : undefined;
  const name = `${QUOTA_REQUESTS}:${role}:${status ?? "all"}:${opts.roomId ?? ""}`;
  return useQuery(queryKey(name, { subject, tenant }), async () => (await api.quotaRequests(role, { roomId: opts.roomId, status })).requests ?? []);
}

export function refreshQuotaRequests(): void {
  invalidateQueries(QUOTA_REQUESTS);
}
