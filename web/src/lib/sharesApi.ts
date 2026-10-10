import { ApiError, req, scoped } from "./api.ts";
import { apiUrl } from "./config";
import { errorDetail } from "./session.ts";
import { tapOnboarding } from "./onboardingEvents.ts";
import type { CreatedShare, PublicShare, ShareView, SiteTicket, TenantShareView } from "./shares.ts";

// Share-link and site endpoints. Member routes go through the authenticated
// transport (`req`); the public ones (publicShareApi) are plain fetches that
// never carry a session — the public share page renders before sign-in and
// must work in a browser that has none.

function sharesPath(roomId: string, artifactId: string): string {
  return `/v1/rooms/${encodeURIComponent(roomId)}/artifacts/${encodeURIComponent(artifactId)}/shares`;
}

export const sharesApi = {
  // Create a link to one version (0 = always the latest). The token comes
  // back once; only its hash is stored.
  create: (roomId: string, artifactId: string, body: { version: number; expiresInHours: number }) =>
    req<CreatedShare>(sharesPath(roomId, artifactId), { method: "POST", body: JSON.stringify(body) }).then(
      tapOnboarding<CreatedShare>({ kind: "share_created", roomId }),
    ),
  // The artifact's links, newest first (revoked/expired ones for 30 days).
  list: (roomId: string, artifactId: string) => req<{ shares: ShareView[] }>(sharesPath(roomId, artifactId)),
  // Creator, room owner/admins, or an org admin.
  revoke: (roomId: string, artifactId: string, shareId: string) =>
    req<void>(`${sharesPath(roomId, artifactId)}/${encodeURIComponent(shareId)}`, { method: "DELETE", raw: true }),
  // Org admin: every active link of the tenant.
  tenantShares: (tenantId?: string) => req<{ shares: TenantShareView[] }>(scoped("/v1/tenant/shares?active=1", tenantId)),
  // A member's frame URL for one site version (ticketed, 1 h).
  siteTicket: (roomId: string, artifactId: string, version: number) =>
    req<SiteTicket>(
      `/v1/rooms/${encodeURIComponent(roomId)}/artifacts/${encodeURIComponent(artifactId)}/versions/${version}/site-ticket`,
      { method: "POST" },
    ),
};

function publicPath(token: string, suffix = ""): string {
  return `/v1/public/shares/${encodeURIComponent(token)}${suffix}`;
}

async function publicFetch(path: string): Promise<Response> {
  const res = await fetch(apiUrl(path), { credentials: "omit", cache: "no-store", referrerPolicy: "no-referrer" });
  if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
  return res;
}

export const publicShareApi = {
  // The link's metadata (counts a view). 404 for every dead link.
  get: async (token: string) => (await (await publicFetch(publicPath(token))).json()) as PublicShare,
  // The version's bytes, for the viewers.
  content: async (token: string) => (await publicFetch(publicPath(token, "/content"))).blob(),
  // The Download button's target (attachment; may redirect to storage).
  downloadUrl: (token: string) => apiUrl(publicPath(token, "/content?download=1")),
};
