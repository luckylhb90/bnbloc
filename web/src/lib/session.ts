// The ONE authenticated transport for the gateway. Every request that carries
// the session bearer — JSON calls, multipart attachment uploads, raw artifact
// uploads, release downloads — goes through authedFetch, so the bearer, the
// super-admin acting-tenant header, and the single 401 → silent re-auth →
// retry-once rule live in exactly one place.

import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import type { PlatformTenant } from "./apiModels.ts";
import { gatewayBase } from "./config.ts";
import { statusText } from "./errors.ts";

let token: string | null = null;

export function setToken(next: string | null) {
  token = next;
}

export function getToken(): string | null {
  return token;
}

// The acting tenant: the platform super-admin's GLOBAL tenant switch, owned
// here and nowhere else. When set it is sent as X-Bnbloc-Acting-Tenant on
// every request (authedFetch), so the whole session acts in that tenant; the
// gateway honors it only for super_admins, and it is its only acting-tenant
// input (no query-string override). It persists per (gateway, subject) so a
// reload keeps it while another member, or another gateway, never inherits
// it; signing out clears it. Views read and change it through
// useActingTenant().
const ACTING_KEY = "bnbloc.actingTenant";

let actingTenant: string | null = null;
let actingScope: string | null = null; // "<gateway>|<subject>" of the stored value
const actingListeners = new Set<() => void>();

function actingStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

function actingKey(scope: string): string {
  return `${ACTING_KEY}:${scope}`;
}

function emitActing() {
  for (const fn of actingListeners) fn();
}

// bindActingTenant loads the acting tenant stored for subject on the current
// gateway; the auth layer calls it whenever the signed-in subject changes
// (null: nobody is signed in, nothing is acted as).
export function bindActingTenant(subject: string | null) {
  actingScope = subject ? `${gatewayBase() || "same-origin"}|${subject}` : null;
  actingTenant = actingScope ? (actingStorage()?.getItem(actingKey(actingScope)) ?? null) : null;
  emitActing();
}

// setActingTenant switches (or, with null, stops) the acting tenant of the
// signed-in subject and persists it.
export function setActingTenant(next: string | null) {
  actingTenant = next || null;
  const store = actingStorage();
  if (store && actingScope) {
    try {
      if (actingTenant) store.setItem(actingKey(actingScope), actingTenant);
      else store.removeItem(actingKey(actingScope));
    } catch {
      // storage full or blocked: the switch lasts until reload
    }
  }
  emitActing();
}

// clearActingTenant forgets the signed-in subject's acting tenant (sign-out).
export function clearActingTenant() {
  setActingTenant(null);
  actingScope = null;
}

export function getActingTenant(): string | null {
  return actingTenant;
}

function subscribeActing(fn: () => void): () => void {
  actingListeners.add(fn);
  return () => actingListeners.delete(fn);
}

// useActingTenant is the views' handle on the acting tenant: its value, the
// tenants a super-admin may act in, and the switch (which reloads, so every
// view re-fetches under the new scope).
export function useActingTenant(isSuper: boolean): {
  readonly acting: string | null;
  readonly tenants: readonly PlatformTenant[];
  readonly switchTo: (tenantId: string | null) => void;
} {
  const acting = useSyncExternalStore(subscribeActing, getActingTenant, getActingTenant);
  const [tenants, setTenants] = useState<PlatformTenant[]>([]);
  useEffect(() => {
    if (!isSuper) return;
    let live = true;
    void import("./api.ts")
      .then(({ api }) => api.listTenants())
      .then((r) => live && setTenants(r.tenants ?? []))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [isSuper]);
  const switchTo = useCallback((tenantId: string | null) => {
    if ((tenantId || null) === getActingTenant()) return;
    setActingTenant(tenantId);
    window.location.reload();
  }, []);
  return { acting: isSuper ? acting : null, tenants, switchTo };
}

// The re-auth handler is installed by the auth layer. It mints a fresh session
// token (the gateway's signing key can rotate on restart), installs it via
// setToken, and reports success.
let reauthHandler: (() => Promise<boolean>) | null = null;
let inflight: Promise<boolean> | null = null;

export function setReauthHandler(fn: (() => Promise<boolean>) | null) {
  reauthHandler = fn;
}

// refreshSession runs the installed re-auth handler, coalescing concurrent
// callers (a burst of 401s from parallel requests, the SSE loop, an explicit
// join) onto ONE refresh so the token is swapped once, not once per caller.
export function refreshSession(): Promise<boolean> {
  const handler = reauthHandler;
  if (!handler) return Promise.resolve(false);
  if (!inflight) {
    inflight = handler()
      .catch(() => false)
      .finally(() => {
        inflight = null;
      });
  }
  return inflight;
}

// Session-minting endpoints never trigger a refresh: a 401 there is the
// answer, and refreshing from inside the refresh would wait on itself.
function isAuthPath(url: string): boolean {
  return url.includes("/v1/auth/");
}

export type FetchImpl = (url: string, init: RequestInit) => Promise<Response>;

// authedFetch sends a request with the session headers and, on a 401 only,
// refreshes the session once and retries with the new bearer. A 403 is an
// authorization answer (not a stale token) and is returned as-is.
export async function authedFetch(
  url: string,
  init: RequestInit = {},
  fetchImpl: FetchImpl = (u, i) => fetch(u, i),
): Promise<Response> {
  const send = () => {
    const headers = new Headers(init.headers);
    if (token) headers.set("Authorization", `Bearer ${token}`);
    if (actingTenant) headers.set("X-Bnbloc-Acting-Tenant", actingTenant);
    return fetchImpl(url, { ...init, headers });
  };
  const first = await send();
  if (first.status !== 401 || isAuthPath(url)) return first;
  if (!(await refreshSession())) return first;
  return send();
}

// errorDetail extracts the message of a failed response. Every bnbloc
// surface answers errors with one body, {"error": "<message>"}
// (internal/httpx.ErrorBody); anything else (an empty body, a proxy's HTML
// page during a restart, a router's plain 404) is never shown raw: it
// becomes a sentence a person can act on.
export async function errorDetail(res: Response): Promise<string> {
  const text = (await res.text().catch(() => "")).trim();
  try {
    const parsed: unknown = JSON.parse(text);
    if (parsed && typeof parsed === "object" && "error" in parsed && typeof parsed.error === "string" && parsed.error) {
      return parsed.error;
    }
  } catch {
    // not the error body
  }
  return statusText(res.status);
}

