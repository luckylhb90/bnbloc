// Share links: the wire shapes of the share routes and the pure helpers the
// share dialog, the organization's "Share links" list and the share page
// format with. No React, no DOM; unit-tested under node. The endpoints live
// in sharesApi.ts. A link is for the room's organization (sign-in
// required) unless its creator lets anyone open it
// (docs/prd/2026-10-13-share-link-sign-in.md).

export type { ShareAccess, ShareTicket, ShareView } from "./wire.gen.ts";
import type { ShareAccess, ShareCreateRequest, ShareView } from "./wire.gen.ts";

/** A share as the org-admin list returns it (every room of the tenant). */
export interface TenantShareView extends ShareView {
  readonly roomId: string;
  readonly artifactTitle: string;
}

export interface CreatedShare {
  readonly share: ShareView;
  readonly token: string;
}

/** What the public share page learns about a link (nothing of the room). */
export interface PublicShare {
  readonly artifact: { readonly kind: string; readonly title: string; readonly fileName?: string };
  readonly version: {
    readonly version: number;
    readonly mimeType: string;
    readonly sizeBytes: number;
    readonly createdAtMs: number;
    readonly summary?: string;
  };
  readonly sharedBy: { readonly displayName: string };
  readonly expiresAtMs: number;
  readonly followsLatest: boolean;
  /** Base of a `site` version's files: gateway-relative, or absolute on
   *  the sites origin. It carries the credential the page was opened with
   *  (the link's token, or the member's share ticket). */
  readonly siteUrl?: string;
}

/** A member's frame URL for one site version (1 h ticket). */
export interface SiteTicket {
  readonly siteUrl: string;
  readonly expiresAtMs: number;
}

/** The tenant policy every member reads (GET /v1/my/tenant-policy). */
export interface TenantPolicy {
  readonly tenantId: string;
  readonly name: string;
  readonly requireSandboxForBorrow: boolean;
  /** Links anyone can open (no sign-in) are off; org links stay allowed. */
  readonly anonymousLinksDisabled: boolean;
}

/** A new link is for the organization's signed-in members. */
export const DEFAULT_SHARE_ACCESS: ShareAccess = "org";

/** The access choices of the share dialog: the organization's members (the
 *  default), or anyone with the link — unavailable while the organization
 *  disables such links. `org` is the organization's name. */
export function shareAccessOptions(
  org: string,
  anonymousLinksDisabled: boolean,
): readonly { readonly value: ShareAccess; readonly label: string; readonly hint: string; readonly disabled: boolean }[] {
  return [
    { value: "org", label: `People in ${orgName(org)} (sign-in required)`, hint: "They sign in with your organization's sign-in (SSO). They don't need to be in this room.", disabled: false },
    {
      value: "anyone",
      label: "Anyone with the link (no sign-in)",
      hint: anonymousLinksDisabled
        ? "Your organization has turned off links anyone can open."
        : "Anyone who gets this link can open it, including people outside your organization.",
      disabled: anonymousLinksDisabled,
    },
  ];
}

/** "People in Acme" / "Anyone with the link": a link's access in a list. */
export function shareAccessLabel(access: ShareAccess, org: string): string {
  return access === "anyone" ? "Anyone with the link" : `People in ${orgName(org)}`;
}

function orgName(org: string): string {
  return org.trim() || "your organization";
}

/** Link lifetimes the dialog offers (the room service caps at 90 days). */
export const SHARE_EXPIRY_OPTIONS: readonly { readonly days: number; readonly label: string }[] = [
  { days: 1, label: "1 day" },
  { days: 7, label: "7 days" },
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
];

export const DEFAULT_SHARE_DAYS = 7;

/** The create request of the dialog's choice. */
export function shareRequest(choice: {
  readonly latest: boolean;
  readonly version: number;
  readonly days: number;
  readonly access: ShareAccess;
}): ShareCreateRequest {
  return { version: choice.latest ? 0 : choice.version, expiresInHours: choice.days * 24, access: choice.access };
}

const DAY_MS = 86_400_000;
const HOUR_MS = 3_600_000;

/** "Expires in 6 days" / "Expires in 3 hours" / "Expired". */
export function expiresLabel(expiresAtMs: number, nowMs: number): string {
  const left = expiresAtMs - nowMs;
  if (left <= 0) return "Expired";
  if (left >= 2 * DAY_MS) return `Expires in ${Math.floor(left / DAY_MS)} days`;
  if (left >= DAY_MS) return "Expires in 1 day";
  const hours = Math.ceil(left / HOUR_MS);
  return hours <= 1 ? "Expires within the hour" : `Expires in ${hours} hours`;
}

/** The one-word state of a link in a list. */
export function shareState(share: Pick<ShareView, "active" | "revokedAtMs" | "expiresAtMs">, nowMs: number): "active" | "revoked" | "expired" {
  if (share.revokedAtMs) return "revoked";
  if (!share.active || share.expiresAtMs <= nowMs) return "expired";
  return "active";
}

/** "Latest version" or "v3". */
export function shareVersionLabel(version: number): string {
  return version > 0 ? `v${version}` : "Latest version";
}

/** "1 view" / "12 views". */
export function viewsLabel(count: number): string {
  return count === 1 ? "1 view" : `${count.toLocaleString("en-US")} views`;
}
