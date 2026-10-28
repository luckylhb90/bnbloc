// The ONE authenticated transport for the gateway. Every request that carries
// the session bearer — JSON calls, multipart attachment uploads, raw artifact
// uploads, release downloads — goes through authedFetch, so the bearer, the
// super-admin acting-tenant header, and the single 401 → silent re-auth →
// retry-once rule live in exactly one place.

import { useCallback, useSyncExternalStore } from "react";
import { gatewayBase } from "./config.ts";
import { statusText } from "./errors.ts";
import { StorageKey, memberKey, readStored, writeStored } from "./storageKeys.ts";

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

let actingTenant: string | null = null;
let actingScope: { readonly gateway: string; readonly subject: string } | null = null; // whose stored value
const actingListeners = new Set<() => void>();

function actingKey(scope: { readonly gateway: string; readonly subject: string }): string {
  return memberKey(StorageKey.actingTenant, scope);
}

function emitActing() {
  for (const fn of actingListeners) fn();
}

// bindActingTenant loads the acting tenant stored for subject on the current
// gateway; the auth layer calls it whenever the signed-in subject changes
// (null: nobody is signed in, nothing is acted as).
export function bindActingTenant(subject: string | null) {
  actingScope = subject ? { gateway: gatewayBase(), subject } : null;
  actingTenant = actingScope ? readStored(actingKey(actingScope)) || null : null;
  emitActing();
}

// setActingTenant switches (or, with null, stops) the acting tenant of the
// signed-in subject and persists it.
export function setActingTenant(next: string | null) {
  actingTenant = next || null;
  // Storage full or blocked: the switch lasts until reload.
  if (actingScope) writeStored(actingKey(actingScope), actingTenant ?? "");
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

// useActingTenant is the views' handle on the acting tenant: its value and
// the switch (which reloads, so every view re-fetches under the new scope).
// The tenants to pick from are lib/queries.ts useTenants.
export function useActingTenant(isSuper: boolean): {
  readonly acting: string | null;
  readonly switchTo: (tenantId: string | null) => void;
} {
  const acting = useSyncExternalStore(subscribeActing, getActingTenant, getActingTenant);
  const switchTo = useCallback((tenantId: string | null) => {
    if ((tenantId || null) === getActingTenant()) return;
    setActingTenant(tenantId);
    window.location.reload();
  }, []);
  return { acting: isSuper ? acting : null, switchTo };
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

// anonymousFetch sends a request with none of this window's session headers:
// a call that is its own credential (a refresh token, an API token) goes to
// the gateway of the account it belongs to, which may not be this window's,
// and must never carry this window's bearer there.
export function anonymousFetch(url: string, init: RequestInit = {}, fetchImpl: FetchImpl = (u, i) => fetch(u, i)): Promise<Response> {
  return fetchImpl(url, init);
}

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
  return (await errorBody(res)).message;
}

// errorBody is errorDetail with what else the one error body names: the
// request field ({"error": …, "field": "<key>"}: a refused component
// setting), the refusal's code and how long to wait (a channel's slow mode).
export async function errorBody(res: Response): Promise<{ readonly message: string; readonly field: string; readonly code: string; readonly retryAfterMs: number }> {
  const text = (await res.text().catch(() => "")).trim();
  try {
    const parsed: unknown = JSON.parse(text);
    if (parsed && typeof parsed === "object" && "error" in parsed && typeof parsed.error === "string" && parsed.error) {
      const str = (k: string) => (k in parsed && typeof (parsed as Record<string, unknown>)[k] === "string" ? ((parsed as Record<string, unknown>)[k] as string) : "");
      const wait = "retryAfterMs" in parsed && typeof parsed.retryAfterMs === "number" ? Math.max(0, parsed.retryAfterMs) : 0;
      return { message: parsed.error, field: str("field"), code: str("code"), retryAfterMs: wait };
    }
  } catch {
    // not the error body
  }
  return { message: statusText(res.status), field: "", code: "", retryAfterMs: 0 };
}

