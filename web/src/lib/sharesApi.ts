import { ApiError, req } from "./api.ts";
import { apiUrl } from "./config";
import { errorDetail } from "./session.ts";
import { tapOnboarding } from "./onboardingEvents.ts";
import type { PublicShare, ShareAccess, ShareCreated, ShareTicket, ShareView, SiteTicket, TenantShareView } from "./shares.ts";
import type { AppVisitorToken, ShareCreateRequest } from "./wire.gen.ts";
import { roomPath } from "./roomId.ts";

// Share-link and site endpoints. Member routes go through the authenticated
// transport (`req`), the share ticket mint included; the link's own routes
// (publicShareApi) are plain fetches that never carry a session. Their
// `link` is the credential the share page opens the link with: the link's
// token, or the member's share ticket in its place (an org link).

function sharesPath(roomId: string, artifactId: string): string {
  return roomPath(roomId, "artifacts", artifactId, "shares");
}

export const sharesApi = {
  // Create a link to one version (0 = always the latest). The link (`url`,
  // the server's: the web never builds one) comes back once; only its
  // token's hash is stored.
  create: (roomId: string, artifactId: string, body: ShareCreateRequest) =>
    req<ShareCreated>(sharesPath(roomId, artifactId), { method: "POST", body: JSON.stringify(body) }).then(
      tapOnboarding<ShareCreated>({ kind: "share_created", roomId }),
    ),
  // Who may open a link: the organization's members, or anyone with it.
  setAccess: (roomId: string, artifactId: string, shareId: string, access: ShareAccess) =>
    req<{ share: ShareView }>(`${sharesPath(roomId, artifactId)}/${encodeURIComponent(shareId)}`, {
      method: "PATCH",
      body: JSON.stringify({ access }),
    }),
  // The signed-in member's ticket for an org link (the token in the body,
  // never the URL); 403 when they are not in the link's organization.
  ticket: (token: string) => req<ShareTicket>("/v1/share-tickets", { method: "POST", body: JSON.stringify({ token }) }),
  // The artifact's links, newest first (revoked/expired ones for 30 days).
  list: (roomId: string, artifactId: string) => req<{ shares: ShareView[] }>(sharesPath(roomId, artifactId)),
  // Creator, room owner/admins, or an org admin.
  revoke: (roomId: string, artifactId: string, shareId: string) =>
    req<void>(`${sharesPath(roomId, artifactId)}/${encodeURIComponent(shareId)}`, { method: "DELETE", as: "none" }),
  // Org admin: every active link of the tenant.
  tenantShares: () => req<{ shares: TenantShareView[] }>("/v1/tenant/shares?active=1"),
  // A member's frame URL for one site version (ticketed, 1 h).
  siteTicket: (roomId: string, artifactId: string, version: number) =>
    req<SiteTicket>(
      roomPath(roomId, "artifacts", artifactId, "versions", version, "site-ticket"),
      { method: "POST" },
    ),
};

function publicPath(link: string, suffix = ""): string {
  return `/v1/public/shares/${encodeURIComponent(link)}${suffix}`;
}

async function publicFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const res = await fetch(apiUrl(path), { ...init, credentials: "omit", cache: "no-store", referrerPolicy: "no-referrer" });
  if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
  return res;
}

export const publicShareApi = {
  // The link's metadata (counts a view). 404 for every dead link; 401 for
  // an org link opened without a share ticket.
  get: async (link: string) => (await (await publicFetch(publicPath(link))).json()) as PublicShare,
  // The version's bytes, for the viewers.
  content: async (link: string) => (await publicFetch(publicPath(link, "/content"))).blob(),
  // The Download button's target (attachment; may redirect to storage).
  downloadUrl: (link: string) => apiUrl(publicPath(link, "/content?download=1")),
  // A visitor token for an anyone link's app (held by lib/appVisitor.ts);
  // prev renews a still-valid one for the same visitor.
  visitor: async (token: string, prev?: string) =>
    (await (
      await publicFetch(publicPath(token, "/visitor"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(prev ? { token: prev } : {}),
      })
    ).json()) as AppVisitorToken,
};
