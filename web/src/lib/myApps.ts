// A member's apps across rooms (Library → "Apps", docs/prd/2026-10-13-app-
// gallery.md §5): the wire shapes of GET /v1/my/apps and the pure helpers
// its cards format with. No React, no DOM; unit-tested under node. Which
// deliverables are apps, and whose they are, is the server's answer.

export type { AppLink, MyApp, MyAppList } from "./wire.gen.ts";
import type { AppLink } from "./wire.gen.ts";
import { expiresLabel, shareAccessLabel, type ShareAccess } from "./shares.ts";

/** A card's link badge: whether the app is shared and how. */
export interface AppLinkBadge {
  readonly label: string;
  /** Every working link, one line each (who can open it, its expiry). */
  readonly title: string;
  /** The widest reach of the working links: anyone (no sign-in),
   *  signed_in (anyone signed in), org (the organization's members). */
  readonly tone: "none" | ShareAccess;
}

/** The badge's word for an app's widest link. */
const BADGE_WORD: Record<ShareAccess, string> = { org: "Org link", signed_in: "Signed-in link", anyone: "Public link" };

/** appLinkBadge sums an app's working links up: "Not shared", "Org link",
 *  "Signed-in link", "Public link", with the count when there are several.
 *  The link that reaches furthest wins the badge (it is the one to
 *  notice): anyone, then anyone signed in, then the organization. */
export function appLinkBadge(links: readonly AppLink[], org: string, nowMs: number): AppLinkBadge {
  if (links.length === 0) return { label: "Not shared", title: "No working share link", tone: "none" };
  const widest = (["anyone", "signed_in"] as const).find((a) => links.some((l) => l.access === a)) ?? "org";
  const word = BADGE_WORD[widest];
  const label = links.length === 1 ? word : `${word} +${links.length - 1}`;
  const title = links.map((l) => `${shareAccessLabel(l.access, org)} · ${expiresLabel(l.expiresAtMs, nowMs)}`).join("\n");
  return { label, title, tone: widest };
}

/** The listing's query string for one page. */
export function myAppsQuery(cursor?: string, limit?: number): string {
  const q = new URLSearchParams();
  if (cursor) q.set("cursor", cursor);
  if (limit) q.set("limit", String(limit));
  const s = q.toString();
  return s ? `?${s}` : "";
}
