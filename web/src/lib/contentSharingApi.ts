import { ApiError, req, scoped } from "./api.ts";
import { apiUrl } from "./config.ts";
import { authedFetch, errorDetail } from "./session.ts";
import type { ContentComponentDetail, ContentPublishRequest } from "./contentComponents.ts";
import type { ContentAdminComponent, ContentPublishReply, ContentReview, ContentShareReply, ContentShares } from "./contentSharing.ts";

const enc = encodeURIComponent;

// Sharing and publishing content components (services/gateway
// content_sharing.go, content_review.go, content_platform.go). tenantId
// scopes a super admin's organization calls like the other tenant routes.
export const contentSharingApi = {
  // The author's side.
  share: (id: string, body: { members?: string[]; roomId?: string; version?: string; note?: string }) =>
    req<ContentShareReply>(`/v1/content-components/${enc(id)}/shares`, { method: "POST", body: JSON.stringify(body) }),
  // The copies made from it and its standing room shares.
  shares: (id: string) => req<ContentShares>(`/v1/content-components/${enc(id)}/shares`),
  // Later joiners of the room get no copy; the copies given stay.
  revokeRoomShare: (id: string, roomId: string) =>
    req<void>(`/v1/content-components/${enc(id)}/shares/rooms/${enc(roomId)}`, { method: "DELETE", raw: true }),
  // A shared copy moves to its author's latest version.
  updateCopy: (id: string) => req<ContentComponentDetail>(`/v1/content-components/${enc(id)}/update`, { method: "POST" }),
  publish: (id: string, scope: "org" | "personal", note?: string) =>
    req<ContentPublishReply>(`/v1/content-components/${enc(id)}/publish`, { method: "POST", body: JSON.stringify({ scope, ...(note ? { note } : {}) }) }),

  // Org admins.
  requests: (all: boolean, tenantId?: string) =>
    req<{ requests: ContentPublishRequest[] }>(scoped(`/v1/tenant/content-requests${all ? "?status=all" : ""}`, tenantId)),
  review: (id: string, tenantId?: string) => req<ContentReview>(scoped(`/v1/tenant/content-requests/${enc(id)}`, tenantId)),
  decide: (id: string, decision: "approve" | "decline", note: string, tenantId?: string) =>
    req<{ request: ContentPublishRequest }>(scoped(`/v1/tenant/content-requests/${enc(id)}/${decision}`, tenantId), {
      method: "POST",
      body: JSON.stringify(note ? { note } : {}),
    }),
  orgContent: (tenantId?: string) => req<{ components: ContentAdminComponent[] }>(scoped("/v1/tenant/content-components", tenantId)),
  setOrgStatus: (id: string, action: "unpublish" | "restore", tenantId?: string) =>
    req<void>(scoped(`/v1/tenant/content-components/${enc(id)}/${action}`, tenantId), { method: "POST", raw: true }),
  removeOrg: (id: string, tenantId?: string) => req<void>(scoped(`/v1/tenant/content-components/${enc(id)}`, tenantId), { method: "DELETE", raw: true }),

  // Super admins.
  platformContent: (scope?: "platform" | "org") =>
    req<{ components: ContentAdminComponent[] }>(`/v1/platform/content-components${scope ? `?scope=${scope}` : ""}`),
  // The signable package of a version: exactly the bytes to sign.
  exportPackage: async (id: string, version: string, publisher?: string): Promise<Blob> => {
    const q = new URLSearchParams({ version });
    if (publisher?.trim()) q.set("publisher", publisher.trim());
    const res = await authedFetch(apiUrl(`/v1/platform/content-components/${enc(id)}/package?${q}`));
    if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
    return res.blob();
  },
  setPlatformStatus: (id: string, action: "unpublish" | "restore") =>
    req<void>(`/v1/platform/content-components/${enc(id)}/${action}`, { method: "POST", raw: true }),
  removePlatform: (id: string) => req<void>(`/v1/platform/content-components/${enc(id)}`, { method: "DELETE", raw: true }),
  // Platform content's organizations: the component tenant scope route.
  setPlatformTenants: (name: string, kind: string, body: { allTenants: true } | { enabledTenants: string[] }) =>
    req<void>(`/v1/platform/components/${enc(name)}/tenants?kind=${enc(kind)}`, { method: "PUT", body: JSON.stringify(body), raw: true }),
};
