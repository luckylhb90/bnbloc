// Host side of a `site` artifact (a static web-app snapshot): where its frame
// loads from, the frame's sandbox, device widths and ticket refresh. Pure (no
// React, no DOM globals) so it is unit-tested under node; SiteViewer wires it
// to the iframe.
//
// Where a site is served from (one file rule set on the gateway):
//   - member: POST …/versions/{v}/site-ticket mints `siteUrl` =
//     `/v1/sites/<ticket>/` (12 h; the ticket in the path is the credential,
//     no session). Browser: that gateway URL. Desktop: the shell's
//     `bnbloc-site` scheme (apps/desktop/src-tauri/src/site_scheme.rs), which
//     proxies the same path to the gateway, so the app CSP only has to admit
//     the scheme in frame-src.
//   - public share page: `siteUrl` from GET /v1/public/shares/{token}.
// Either way the gateway's response CSP is `sandbox allow-scripts …` (an
// opaque origin even when opened top-level) and the iframe sandbox below
// never grants allow-same-origin.
//
// An interactive app's SDK (`~app/sdk.js`) calls `~app/…` relative to the
// site's own URL, so under a member ticket it acts as the member with no
// token of its own. It asks its parent for a visitor token at load; the
// member's viewer answers nothing (only a public link's page holds one:
// lib/appVisitor.ts), and the SDK asks again only when a call is refused
// for a missing visitor token, which a ticket never is. The desktop's
// scheme forwards only reads so far
// (docs/bugs/2026-10-13-desktop-site-frame-drops-app-api.md).

import { schemeUrl, type ConvertFileSrc } from "./schemeUrl.ts";
/** Desktop URI scheme proxying `/v1/sites/…` to the gateway. */
export const SITE_SCHEME = "bnbloc-site";

/** The site frame's sandbox: scripts, forms, popups, dialogs and downloads,
 *  never allow-same-origin (the app's origin, cookies and storage stay out of
 *  reach) and never top navigation. */
export const SITE_SANDBOX = "allow-scripts allow-forms allow-popups allow-modals allow-downloads";

/** Device-width presets of the site toolbar ("fill" = responsive). */
export const SITE_WIDTHS: readonly { readonly value: string; readonly label: string }[] = [
  { value: "fill", label: "Responsive" },
  { value: "390", label: "Phone 390" },
  { value: "820", label: "Tablet 820" },
  { value: "1280", label: "Desktop 1280" },
];

/** A member site ticket is re-minted this long before it expires. */
export const SITE_TICKET_SLACK_MS = 5 * 60 * 1000;

export type { ConvertFileSrc };

export interface SiteFrameEnv {
  /** Tauri's convertFileSrc (desktop shell only). */
  readonly convertFileSrc?: ConvertFileSrc;
  /** Prefixes a gateway path with the configured gateway base. */
  readonly apiUrl: (path: string) => string;
  readonly baseURI: string;
}

const MEMBER_SITE_PATH = /^\/v1\/sites\/[A-Za-z0-9._~-]+\//;

/** The gateway URL of a site (absolute): what "open in new tab" opens. */
export function siteGatewayUrl(siteUrl: string, env: Pick<SiteFrameEnv, "apiUrl" | "baseURI">): string {
  return new URL(env.apiUrl(siteUrl), env.baseURI).href;
}

/** The iframe src of a site. In the desktop shell a member site URL goes
 *  through the `bnbloc-site` scheme (schemeUrl keeps the path's slashes).
 *  Anything else is the gateway URL. */
export function siteFrameUrl(siteUrl: string, env: SiteFrameEnv): string {
  if (env.convertFileSrc && MEMBER_SITE_PATH.test(siteUrl)) {
    return schemeUrl(env.convertFileSrc, SITE_SCHEME, siteUrl);
  }
  return siteGatewayUrl(siteUrl, env);
}

/** Milliseconds until a ticket expiring at `expiresAtMs` must be re-minted
 *  (0 = now); null when the URL does not expire. */
export function siteTicketRefreshDelay(expiresAtMs: number | undefined, nowMs: number): number | null {
  if (!expiresAtMs) return null;
  return Math.max(0, expiresAtMs - SITE_TICKET_SLACK_MS - nowMs);
}
