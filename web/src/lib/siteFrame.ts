// Host side of a `site` artifact (a static web-app snapshot): where its frame
// loads from, the frame's sandbox, device widths and ticket refresh. Pure (no
// React, no DOM globals) so it is unit-tested under node; SiteViewer wires it
// to the iframe.
//
// Where a site is served from (one file rule set on the gateway). The
// gateway hands out `siteUrl`: a path on its own origin, or an absolute
// URL on the deployment's sites origin (BNBLOC_SITES_ORIGIN, the only
// origin serving sites then). The web never decides the host.
//   - member: POST …/versions/{v}/site-ticket mints `siteUrl` for
//     `/v1/sites/<ticket>/` (12 h; the ticket in the path is the credential,
//     no session). Browser: that URL. Desktop: the shell's `bnbloc-site`
//     scheme (apps/desktop/src-tauri/src/site_scheme.rs) with the URL's
//     path, which the shell proxies to the gateway's sites (the sites
//     origin when the gateway names one), so the app CSP only has to admit
//     the scheme in frame-src.
//   - public share page: `siteUrl` from GET /v1/public/shares/{token}.
// Either way the gateway's response CSP is `sandbox allow-scripts …` (an
// opaque origin even when opened top-level) and the iframe sandbox below
// never grants allow-same-origin.
//
// An interactive app's SDK (`~app/sdk.js`) calls `~app/…` relative to the
// site's own URL, so under a member ticket it acts as the member with no
// token of its own (the desktop scheme forwards every method of the app
// API and relays its change feed). It asks its parent for a visitor token
// at load; the member's viewer answers nothing (only a public link's page
// holds one: lib/appVisitor.ts), and the SDK asks again only when a call
// is refused for a missing visitor token, which a ticket never is.

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
const ABSOLUTE_URL = /^[a-z][a-z0-9+.-]*:/i;

/** The URL of a site (absolute): what "open in new tab" opens and a
 *  browser frames. A `siteUrl` on the sites origin is already absolute;
 *  a path is on the gateway. */
export function siteGatewayUrl(siteUrl: string, env: Pick<SiteFrameEnv, "apiUrl" | "baseURI">): string {
  return ABSOLUTE_URL.test(siteUrl) ? siteUrl : new URL(env.apiUrl(siteUrl), env.baseURI).href;
}

/** The iframe src of a site. In the desktop shell a member site goes
 *  through the `bnbloc-site` scheme with its path (schemeUrl keeps the
 *  slashes); the shell forwards it to wherever the gateway serves sites.
 *  Anything else is the site's URL. */
export function siteFrameUrl(siteUrl: string, env: SiteFrameEnv): string {
  const url = new URL(siteGatewayUrl(siteUrl, env));
  const path = url.pathname + url.search;
  if (env.convertFileSrc && MEMBER_SITE_PATH.test(path)) {
    return schemeUrl(env.convertFileSrc, SITE_SCHEME, path);
  }
  return url.href;
}

/** Milliseconds until a ticket expiring at `expiresAtMs` must be re-minted
 *  (0 = now); null when the URL does not expire. */
export function siteTicketRefreshDelay(expiresAtMs: number | undefined, nowMs: number): number | null {
  if (!expiresAtMs) return null;
  return Math.max(0, expiresAtMs - SITE_TICKET_SLACK_MS - nowMs);
}
