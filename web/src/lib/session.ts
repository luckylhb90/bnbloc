// The ONE authenticated transport for the gateway. Every request that carries
// the session bearer — JSON calls, multipart attachment uploads, raw artifact
// uploads, release downloads — goes through authedFetch, so the bearer, the
// super-admin acting-tenant header, and the single 401 → silent re-auth →
// retry-once rule live in exactly one place.

let token: string | null = null;

export function setToken(next: string | null) {
  token = next;
}

export function getToken(): string | null {
  return token;
}

// actingTenant is the platform super-admin's GLOBAL tenant switch. When set, it
// is sent as X-Bnbloc-Acting-Tenant on every request so the whole session acts
// as that tenant. The gateway honors it only for super_admins and ignores it
// for everyone else, so it is always safe to send. Seeded from localStorage so
// the switch survives reloads (the Shell writes it there on switch).
let actingTenant: string | null =
  typeof localStorage !== "undefined" ? localStorage.getItem("bnbloc.actingTenant") : null;

export function setActingTenant(next: string | null) {
  actingTenant = next;
}

export function getActingTenant(): string | null {
  return actingTenant;
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

// errorDetail extracts the gateway's error text from a failed response: the
// JSON `error`/`message` field when present, else a plain-text body. An empty
// body or a proxy's HTML error page (a 502 while a service restarts) is never
// shown raw: it becomes a sentence a person can act on.
export async function errorDetail(res: Response): Promise<string> {
  const text = (await res.text().catch(() => "")).trim();
  if (!text || text.startsWith("<")) return statusMessage(res.status);
  try {
    const parsed: unknown = JSON.parse(text);
    if (parsed && typeof parsed === "object") {
      const error = "error" in parsed ? parsed.error : undefined;
      const message = "message" in parsed ? parsed.message : undefined;
      if (typeof error === "string" && error) return error;
      if (typeof message === "string" && message) return message;
    }
    return text;
  } catch {
    return text;
  }
}

// statusMessage is the fallback text of a failed response with no usable body.
export function statusMessage(status: number): string {
  if (status === 502 || status === 503 || status === 504) {
    return `The server is temporarily unavailable (${status}). Try again in a moment.`;
  }
  return `Request failed (${status})`;
}
