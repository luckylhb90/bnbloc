// Public share links: the wire shapes of the share routes and the pure
// helpers the share dialog, the organization's "Public links" list and the
// public share page format with. No React, no DOM; unit-tested under node.
// The endpoints live in sharesApi.ts.

/** One share link of a deliverable (the secret token is never listed; it is
 *  returned once, at create). */
export interface ShareView {
  readonly shareId: string;
  readonly artifactId: string;
  /** 0 = always the latest version. */
  readonly version: number;
  readonly createdBy: string;
  readonly createdAtMs: number;
  readonly expiresAtMs: number;
  readonly revokedAtMs?: number;
  readonly revokedBy?: string;
  readonly viewCount: number;
  readonly lastViewedAtMs?: number;
  readonly active: boolean;
}

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
  /** Gateway-relative base of a `site` version's files. */
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
  readonly requireSandboxForBorrow: boolean;
  readonly publicSharesDisabled: boolean;
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
export function shareRequest(choice: { readonly latest: boolean; readonly version: number; readonly days: number }): {
  version: number;
  expiresInHours: number;
} {
  return { version: choice.latest ? 0 : choice.version, expiresInHours: choice.days * 24 };
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
