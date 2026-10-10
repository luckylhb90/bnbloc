import { useEffect, useState } from "react";
import { api } from "./api.ts";
import { useAuth } from "./auth";
import type { TenantPolicy } from "./shares.ts";

// The member-readable tenant policy (GET /v1/my/tenant-policy), cached per
// session tenant so every share button and the sandbox settings share one
// request. An org admin changing the policy invalidates it.

const TTL_MS = 5 * 60 * 1000;
let cached: { tenant: string; at: number; policy: Promise<TenantPolicy> } | null = null;
const listeners = new Set<() => void>();

export function loadTenantPolicy(tenant: string | null): Promise<TenantPolicy> {
  const key = tenant ?? "";
  if (cached && cached.tenant === key && Date.now() - cached.at < TTL_MS) return cached.policy;
  const policy = api.myTenantPolicy();
  const entry = { tenant: key, at: Date.now(), policy };
  cached = entry;
  // A failure is not cached: the next consumer retries.
  policy.catch(() => {
    if (cached === entry) cached = null;
  });
  return policy;
}

/** Drops the cached policy and re-reads it in every mounted consumer. */
export function invalidateTenantPolicy(): void {
  cached = null;
  for (const l of listeners) l();
}

/** The session tenant's policy; null while loading or when unreadable. */
export function useTenantPolicy(): TenantPolicy | null {
  const { tenant } = useAuth();
  const [rev, setRev] = useState(0);
  const [policy, setPolicy] = useState<TenantPolicy | null>(null);
  useEffect(() => {
    const listener = () => setRev((n) => n + 1);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);
  useEffect(() => {
    let cancelled = false;
    loadTenantPolicy(tenant)
      .then((p) => {
        if (!cancelled) setPolicy(p);
      })
      .catch(() => {
        if (!cancelled) setPolicy(null);
      });
    return () => {
      cancelled = true;
    };
  }, [tenant, rev]);
  return policy;
}

/** Whether members may create public share links (hidden until known). */
export function useSharingEnabled(): boolean {
  const policy = useTenantPolicy();
  return policy !== null && !policy.publicSharesDisabled;
}
