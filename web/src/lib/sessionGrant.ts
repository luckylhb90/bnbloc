// Session grants and their renewal, free of React so the decisions are unit
// tested (sessionGrant.test.ts). auth.tsx owns the state wiring; this module
// owns WHAT a grant persists as, HOW a grant lands in the member's chosen
// tenant, and WHEN/HOW an expiring session is renewed.
//
// Every login path (direct login, browser SSO fragment, desktop SSO code
// exchange, refresh) yields the same grant: a 1h session bearer plus a ~30d
// refresh token. The refresh token is never a bearer; it only mints new
// grants through POST /v1/auth/refresh, each carrying a rotated refresh token
// that replaces the stored one.

export const SESSION_KEY = "bnbloc.session";

export interface Grant {
  sessionToken: string;
  refreshToken?: string;
  subject: string;
  tenant?: string;
  role?: string;
}

// SessionRecord is the persisted session (localStorage SESSION_KEY).
export interface SessionRecord {
  token: string;
  refreshToken: string | null;
  subject: string | null;
  tenant: string | null;
  role: string | null;
}

export function recordFromGrant(g: Grant): SessionRecord {
  return {
    token: g.sessionToken,
    refreshToken: g.refreshToken || null,
    subject: g.subject,
    tenant: g.tenant || null,
    role: g.role || null,
  };
}

export function parseSessionRecord(raw: string | null | undefined): SessionRecord | null {
  if (!raw) return null;
  try {
    const p = JSON.parse(raw) as Record<string, unknown>;
    const str = (v: unknown) => (typeof v === "string" && v.trim() ? v : null);
    const token = str(p.token);
    if (!token) return null;
    return {
      token,
      refreshToken: str(p.refreshToken),
      subject: str(p.subject),
      tenant: str(p.tenant),
      role: str(p.role),
    };
  } catch {
    return null;
  }
}

// decodeTokenClaims reads a signed session token's payload (the claims are
// the transport for tenant/role on the SSO callback path, where no login
// response object exists, and for the expiry the proactive renewal keys on).
// bnbloc tokens are TWO segments — base64url(claims JSON) + HMAC — not a
// 3-segment JWT, so the payload is the FIRST segment. Signature verification
// is the gateway's job; a tampered token fails API auth regardless.
export interface TokenClaims {
  sub?: string;
  tenant?: string;
  role?: string;
  exp?: number;
}

export function decodeTokenClaims(token: string): TokenClaims {
  const part = token.split(".")[0];
  if (!part) return {};
  try {
    const b64 = part.replace(/-/g, "+").replace(/_/g, "/");
    const json = decodeURIComponent(
      atob(b64)
        .split("")
        .map((c) => "%" + c.charCodeAt(0).toString(16).padStart(2, "0"))
        .join(""),
    );
    return JSON.parse(json) as TokenClaims;
  } catch {
    return {};
  }
}

// grantFromToken builds a grant from a bare session token (the browser SSO
// fragment), restoring tenant/role from the token's claims so an SSO sign-in
// carries the same scopes as a direct login.
export function grantFromToken(token: string, subject: string | null, refreshToken: string | null): Grant {
  const claims = decodeTokenClaims(token);
  return {
    sessionToken: token,
    refreshToken: refreshToken ?? undefined,
    subject: subject || claims.sub || "user",
    tenant: claims.tenant,
    role: claims.role,
  };
}

// SsoFragment is what the gateway's SSO redirect carries in the URL hash:
// the browser flow `#sessionToken=…&refreshToken=…&subject=…`, or the
// desktop flow's one-time `#ssoCode=…` (redeemed via /v1/auth/sso/exchange).
export interface SsoFragment {
  ssoCode: string | null;
  grant: Grant | null;
}

export function parseSsoFragment(hash: string): SsoFragment {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const token = params.get("sessionToken");
  return {
    ssoCode: params.get("ssoCode") || null,
    grant: token ? grantFromToken(token, params.get("subject"), params.get("refreshToken") || null) : null,
  };
}

export interface EstablishDeps {
  setToken: (token: string) => void;
  switchTenant: (tenantId: string) => Promise<{ sessionToken: string; tenant?: string; role?: string }>;
}

// establishSession lands a freshly minted grant in `wantedFor(subject)` (when
// it differs from the grant's tenant), installing each token before the next
// call so the tenant switch authorizes with the fresh bearer. Login, SSO and
// refresh all mint the member's DEFAULT tenant (the refresh token is
// subject-scoped), so every path funnels through here and the tenant choice
// can never drift between them. The tenant switch keeps the grant's refresh
// token: switch-tenant does not mint one.
export async function establishSession(
  mint: () => Promise<Grant>,
  wantedFor: (subject: string) => string | null,
  deps: EstablishDeps,
  onSwitchFailed?: (status: number, subject: string) => void,
): Promise<SessionRecord> {
  const fresh = await mint();
  const record = recordFromGrant(fresh);
  deps.setToken(record.token);
  const wanted = wantedFor(fresh.subject);
  if (wanted && record.tenant !== wanted) {
    try {
      const switched = await deps.switchTenant(wanted);
      record.token = switched.sessionToken;
      record.tenant = switched.tenant ?? wanted;
      record.role = switched.role ?? record.role;
      deps.setToken(record.token);
    } catch (e) {
      // The workspace is gone or no longer permitted: stay on the default
      // rather than leaving the session unauthenticated.
      onSwitchFailed?.(errorStatus(e), fresh.subject);
    }
  }
  return record;
}

export interface RenewDeps {
  // The persisted session (refresh token, subject) at renewal time.
  stored: SessionRecord | null;
  // The signed-in subject from state (may lead storage on first render).
  subject: string | null;
  directLoginAvailable: boolean;
  refresh: (refreshToken: string) => Promise<Grant>;
  login: (subject: string) => Promise<Grant>;
  // Lands the minted grant in the member's tenant and installs/persists it.
  establish: (mint: () => Promise<Grant>) => Promise<unknown>;
  logout: () => void;
}

// renewSession is the ONE silent re-auth decision. A stored refresh token
// mints the new grant; without one, only a gateway that still offers direct
// login can re-mint (dev verifier); otherwise the session is over. A refusal
// (401/403) signs out so the app shows the sign-in screen instead of
// rendering empty views with a dead token; a network failure keeps the
// session so a later request retries.
export async function renewSession(d: RenewDeps): Promise<boolean> {
  const refreshToken = d.stored?.refreshToken ?? null;
  const subject = d.subject || d.stored?.subject || null;
  let mint: () => Promise<Grant>;
  if (refreshToken) mint = () => d.refresh(refreshToken);
  else if (subject && d.directLoginAvailable) mint = () => d.login(subject);
  else {
    d.logout();
    return false;
  }
  try {
    await d.establish(mint);
    return true;
  } catch (e) {
    const status = errorStatus(e);
    if (status === 401 || status === 403) d.logout();
    return false;
  }
}

// How long before the session bearer's expiry the proactive renewal fires, so
// a long-lived SSE stream or upload never runs into a dead token.
export const RENEW_AHEAD_MS = 5 * 60 * 1000;

// renewDelayMs is the wait before proactively renewing `token`, or null when
// the token carries no expiry. An already-due token renews immediately.
export function renewDelayMs(token: string, nowMs: number): number | null {
  const exp = decodeTokenClaims(token).exp;
  if (typeof exp !== "number" || !Number.isFinite(exp)) return null;
  return Math.max(0, exp * 1000 - RENEW_AHEAD_MS - nowMs);
}

// errorStatus reads the HTTP status off an ApiError (duck-typed so this
// module stays free of the api client); 0 means no response (network).
function errorStatus(e: unknown): number {
  if (e && typeof e === "object" && "status" in e && typeof e.status === "number") return e.status;
  return 0;
}
