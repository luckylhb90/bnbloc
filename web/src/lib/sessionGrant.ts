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

export interface Grant {
  sessionToken: string;
  refreshToken?: string;
  // A test account's long-lived credential (an API token): its sessions are
  // minted from it, so a grant minted that way carries it on.
  apiToken?: string;
  subject: string;
  tenant?: string;
  role?: string;
}

// SessionRecord is one account's session as the vault keeps it (lib/vault.ts).
// It renews from its refresh token (a person who signed in) or, for a test
// account (docs/prd/2026-10-22-multi-account.md), from its API token.
export interface SessionRecord {
  token: string;
  refreshToken: string | null;
  apiToken: string | null;
  subject: string | null;
  tenant: string | null;
  role: string | null;
}

export function recordFromGrant(g: Grant): SessionRecord {
  return {
    token: g.sessionToken,
    refreshToken: g.refreshToken || null,
    apiToken: g.apiToken || null,
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
      apiToken: str(p.apiToken),
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

/** What mints a session from a credential: the refresh and the API token exchange. */
export interface MintDeps {
  refresh: (refreshToken: string) => Promise<Grant>;
  // Exchanges a test account's API token for a session (no refresh token).
  exchangeApiToken: (apiToken: string) => Promise<Grant>;
}

// credentialMint is how a record mints its next grant from the credential it
// holds: its refresh token first, else a test account's API token (the grant
// keeps the API token, so the next renewal has it). null when it holds
// neither. Every renewal (renewal.ts) and every switch to another account
// mints through it.
export function credentialMint(record: SessionRecord | null, d: MintDeps): (() => Promise<Grant>) | null {
  const refreshToken = record?.refreshToken ?? null;
  if (refreshToken) return () => d.refresh(refreshToken);
  const apiToken = record?.apiToken ?? null;
  if (apiToken) return () => d.exchangeApiToken(apiToken).then((g) => ({ ...g, apiToken }));
  return null;
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
export function errorStatus(e: unknown): number {
  if (e && typeof e === "object" && "status" in e && typeof e.status === "number") return e.status;
  return 0;
}
