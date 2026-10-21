import { req } from "./api.ts";
import type { ContentComponentDetail, ContentPublishRequest } from "./contentComponents.ts";
import { changing } from "./contentChanges.ts";
import type { ContentAdminComponent, ContentPublishReply, ContentReview, ContentShareReply, ContentShares } from "./contentSharing.ts";

const enc = encodeURIComponent;

// Sharing and publishing content components (services/gateway
// content_sharing.go, content_review.go, content_platform.go). Organization
// calls act in the session's tenant (a super admin's acting tenant). Every
// mutation announces itself (lib/contentChanges.ts): an id names no kind, so
// every list showing content reads it again.
export const contentSharingApi = {
  // The author's side.
  share: (id: string, body: { members?: string[]; roomId?: string; version?: string; note?: string }) =>
    changing(req<ContentShareReply>(`/v1/content-components/${enc(id)}/shares`, { method: "POST", body: JSON.stringify(body) }), null),
  // The copies made from it and its standing room shares.
  shares: (id: string) => req<ContentShares>(`/v1/content-components/${enc(id)}/shares`),
  // Later joiners of the room get no copy; the copies given stay.
  revokeRoomShare: (id: string, roomId: string) =>
    changing(req<void>(`/v1/content-components/${enc(id)}/shares/rooms/${enc(roomId)}`, { method: "DELETE", as: "none" }), null),
  // A shared copy moves to its author's latest version.
  updateCopy: (id: string) =>
    changing(req<ContentComponentDetail>(`/v1/content-components/${enc(id)}/update`, { method: "POST" }), (d) => d.component.kind),
  // version: the one to publish (default the latest; a publish card names
  // its own). scope "personal" withdraws it from the organization.
  publish: (id: string, scope: "org" | "personal", note?: string, version?: string) =>
    changing(
      req<ContentPublishReply>(`/v1/content-components/${enc(id)}/publish`, {
        method: "POST",
        body: JSON.stringify({ scope, ...(note ? { note } : {}), ...(version ? { version } : {}) }),
      }),
      null,
    ),

  // Org admins.
  requests: (all: boolean) =>
    req<{ requests: ContentPublishRequest[] }>(`/v1/tenant/content-requests${all ? "?status=all" : ""}`),
  review: (id: string) => req<ContentReview>(`/v1/tenant/content-requests/${enc(id)}`),
  decide: (id: string, decision: "approve" | "decline", note: string) =>
    changing(
      req<{ request: ContentPublishRequest }>(`/v1/tenant/content-requests/${enc(id)}/${decision}`, {
        method: "POST",
        body: JSON.stringify(note ? { note } : {}),
      }),
      (r) => r.request.kind,
    ),
  orgContent: () => req<{ components: ContentAdminComponent[] }>("/v1/tenant/content-components"),
  setOrgStatus: (id: string, action: "unpublish" | "restore") =>
    changing(req<void>(`/v1/tenant/content-components/${enc(id)}/${action}`, { method: "POST", as: "none" }), null),
  removeOrg: (id: string) => changing(req<void>(`/v1/tenant/content-components/${enc(id)}`, { method: "DELETE", as: "none" }), null),

  // Super admins.
  platformContent: (scope?: "platform" | "org") =>
    req<{ components: ContentAdminComponent[] }>(`/v1/platform/content-components${scope ? `?scope=${scope}` : ""}`),
  // The signable package of a version: exactly the bytes to sign.
  exportPackage: (id: string, version: string, publisher?: string): Promise<Blob> => {
    const q = new URLSearchParams({ version });
    if (publisher?.trim()) q.set("publisher", publisher.trim());
    return req<Blob>(`/v1/platform/content-components/${enc(id)}/package?${q}`, { as: "blob" });
  },
  setPlatformStatus: (id: string, action: "unpublish" | "restore") =>
    changing(req<void>(`/v1/platform/content-components/${enc(id)}/${action}`, { method: "POST", as: "none" }), null),
  removePlatform: (id: string) => changing(req<void>(`/v1/platform/content-components/${enc(id)}`, { method: "DELETE", as: "none" }), null),
  // Platform content's organizations: the component tenant scope route.
  setPlatformTenants: (name: string, kind: string, body: { allTenants: true } | { enabledTenants: string[] }) =>
    changing(req<void>(`/v1/platform/components/${enc(name)}/tenants?kind=${enc(kind)}`, { method: "PUT", body: JSON.stringify(body), as: "none" }), null),
};
