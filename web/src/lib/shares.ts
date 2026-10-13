// Share links: the wire shapes of the share routes and the pure helpers the
// share dialog, the organization's "Share links" list and the share page
// format with. No React, no DOM; unit-tested under node. The endpoints live
// in sharesApi.ts. A link is for the room's organization (sign-in
// required) unless its creator lets anyone signed in, or anyone at all,
// open it (docs/prd/2026-10-13-share-link-sign-in.md).

export type { ShareAccess, ShareCreated, ShareTicket, ShareView, TenantPolicy } from "./wire.gen.ts";
import type { ShareAccess, ShareCreateRequest, ShareTicket, ShareView, TenantPolicy } from "./wire.gen.ts";
import { ApiError, errorReason } from "./errors.ts";
import { ShareAccessValues } from "./wire.gen.ts";

/** A share as the org-admin list returns it (every room of the tenant). */
export interface TenantShareView extends ShareView {
  readonly roomId: string;
  readonly artifactTitle: string;
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

/** What a link shares: a file, a site, or an interactive app. */
export type ShareTarget = "file" | "site" | "app";

/** The share dialog's line on what the people a link reaches get. */
export function shareTargetDescription(target: ShareTarget): string {
  switch (target) {
    case "app":
      return "People you send the link to open this app full-screen and use it. Nothing else of the room is shown.";
    case "site":
      return "People you send the link to open this site full-screen. Nothing else of the room is shown.";
    default:
      return "People you send the link to can view and download this file. Nothing else of the room is shown.";
  }
}

/** A new link is for the organization's signed-in members. */
export const DEFAULT_SHARE_ACCESS: ShareAccess = "org";

/** The access modes the organization allows: the tenant policy's answer
 *  (the server decides; the web never re-reads the switches). Until the
 *  policy is known, only the default. */
export function allowedShareAccess(policy: Pick<TenantPolicy, "shareAccess"> | null | undefined): readonly ShareAccess[] {
  return policy?.shareAccess ?? [DEFAULT_SHARE_ACCESS];
}

/** The access a new link is created with: the choice, while the
 *  organization allows it, else the default. */
export function chosenShareAccess(choice: ShareAccess, allowed: readonly ShareAccess[]): ShareAccess {
  return allowed.includes(choice) ? choice : DEFAULT_SHARE_ACCESS;
}

interface ShareAccessWords {
  /** The dialog's choice ("People in Acme (sign-in required)"). */
  option: (org: string) => string;
  /** What the people the link reaches need. */
  hint: string;
  /** Why the choice is unavailable. */
  off: string;
  /** A link's access in a list ("People in Acme"). */
  label: (org: string) => string;
  /** The toast after switching a link to it. */
  changed: string;
}

const SHARE_ACCESS: Record<ShareAccess, ShareAccessWords> = {
  org: {
    option: (org) => `People in ${orgName(org)} (sign-in required)`,
    hint: "They sign in with your organization's sign-in. They don't need to be in this room.",
    off: "",
    label: (org) => `People in ${orgName(org)}`,
    changed: "Only people in the organization can open the link now",
  },
  signed_in: {
    option: () => "Anyone signed in (any organization)",
    hint: "Anyone who can sign in here opens it, including people in other organizations or in none.",
    off: "Your organization has turned off links for people outside it.",
    label: () => "Anyone signed in",
    changed: "Anyone signed in can open the link now",
  },
  anyone: {
    option: () => "Anyone with the link (no sign-in)",
    hint: "Anyone who gets this link can open it, including people outside your organization.",
    off: "Your organization has turned off links anyone can open.",
    label: () => "Anyone with the link",
    changed: "Anyone with the link can open it now",
  },
};

/** The access choices of the share dialog, narrowest reach first: the
 *  organization's members (the default), anyone signed in, anyone with the
 *  link. A mode the organization does not allow is unavailable. `org` is
 *  the organization's name. */
export function shareAccessOptions(
  org: string,
  allowed: readonly ShareAccess[],
): readonly { readonly value: ShareAccess; readonly label: string; readonly hint: string; readonly disabled: boolean }[] {
  return ShareAccessValues.map((value) => {
    const words = SHARE_ACCESS[value];
    const disabled = value !== DEFAULT_SHARE_ACCESS && !allowed.includes(value);
    return { value, label: words.option(org), hint: disabled ? words.off : words.hint, disabled };
  });
}

/** "People in Acme" / "Anyone signed in" / "Anyone with the link": a
 *  link's access in a list. */
export function shareAccessLabel(access: ShareAccess, org: string): string {
  return SHARE_ACCESS[access].label(org);
}

/** The toast after a link's access changed to access. */
export function shareAccessChanged(access: ShareAccess): string {
  return SHARE_ACCESS[access].changed;
}

/** What the share page does with a link (PublicSharePage): open it, send
 *  the person to sign in, or tell them it is only for an organization they
 *  are not in. `link` is the credential the page opens it with: the token
 *  (an anyone link, `visitor`), or the signed-in person's share ticket. */
export type ShareOpening =
  | { readonly kind: "signin" }
  | { readonly kind: "orgOnly"; readonly message: string }
  | { readonly kind: "ready"; readonly share: PublicShare; readonly link: string; readonly visitor: boolean };

/** The share page's requests: the link's metadata by credential, and the
 *  signed-in person's ticket mint. */
export interface ShareOpenApi {
  get(credential: string): Promise<PublicShare>;
  ticket(token: string): Promise<Pick<ShareTicket, "ticket">>;
}

/** openShareLink opens the link of token. Anonymously first: an anyone
 *  link answers. A link that needs sign-in (org or signed_in) answers 401:
 *  without a session the page sends the person to sign in; with one it
 *  mints a share ticket and opens the link with it. Only an org link's
 *  mint refuses a signed-in person (403, "only for members of <org>");
 *  a signed_in link takes any session. Other failures are thrown. */
export async function openShareLink(token: string, signedIn: boolean, api: ShareOpenApi): Promise<ShareOpening> {
  try {
    return { kind: "ready", share: await api.get(token), link: token, visitor: true };
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 401) throw e;
  }
  if (!signedIn) return { kind: "signin" };
  try {
    const { ticket } = await api.ticket(token);
    return { kind: "ready", share: await api.get(ticket), link: ticket, visitor: false };
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) return { kind: "orgOnly", message: errorReason(e) };
    if (e instanceof ApiError && e.status === 401) return { kind: "signin" };
    throw e;
  }
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
